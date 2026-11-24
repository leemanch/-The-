package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var assets embed.FS

//go:embed catalog.txt
var initialCatalog string
var db *sql.DB
var secure = os.Getenv("COOKIE_SECURE") == "true"

const schema = `
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY AUTOINCREMENT, first TEXT NOT NULL, last TEXT NOT NULL, identity TEXT UNIQUE NOT NULL, salt BLOB NOT NULL, hash BLOB NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, uid INTEGER REFERENCES users ON DELETE CASCADE, expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS session_expiry ON sessions(expires);
CREATE TABLE IF NOT EXISTS catalog(name TEXT NOT NULL, folded TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS dishes(id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER NOT NULL REFERENCES users ON DELETE CASCADE, name TEXT NOT NULL, taste INTEGER CHECK(taste BETWEEN 1 AND 10), looks INTEGER CHECK(looks BETWEEN 1 AND 10), comment TEXT NOT NULL DEFAULT '', rated INTEGER);
CREATE INDEX IF NOT EXISTS user_dishes ON dishes(uid,rated,id);
CREATE INDEX IF NOT EXISTS recent ON dishes(rated DESC,id DESC);
`

func main() {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "data/byk.db"
	}
	must(os.MkdirAll(filepath.Dir(path), 0700))
	var err error
	db, err = sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	must(err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	_, err = db.Exec("PRAGMA journal_mode=WAL")
	must(err)
	_, err = db.Exec(schema)
	must(err)
	must(seedCatalog())
	if len(os.Args) == 3 && os.Args[1] == "import" {
		must(importCatalog(os.Args[2]))
		return
	}
	if len(os.Args) != 1 {
		log.Fatal("Команда: byk import catalog.txt")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", state)
	mux.HandleFunc("GET /api/catalog", catalog)
	mux.HandleFunc("GET /api/reviews", reviews)
	for route, fn := range map[string]action{"register": register, "login": login, "logout": logout, "delete": removeUser, "add": add, "rate": rate, "remove": removeDish, "reviews/edit": editReview, "reviews/delete": deleteReview} {
		mux.HandleFunc("POST /api/"+route, post(route, fn))
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "database", 503)
			return
		}
		fmt.Fprint(w, "ok")
	})
	dir, err := fs.Sub(assets, "web")
	must(err)
	mux.Handle("GET /", http.FileServer(http.FS(dir)))
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
	log.Printf("Listening on %s", addr)
	must((&http.Server{Addr: addr, Handler: handler, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}).ListenAndServe())
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func valid(s string, max int) bool { n := utf8.RuneCountInString(s); return n >= 1 && n <= max }
func seedCatalog() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS seeds(name TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	result, err := tx.Exec("INSERT OR IGNORE INTO seeds VALUES('kaluga-kirova-20261124')")
	if err != nil {
		return err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if added > 0 {
		if err = readCatalog(tx, strings.NewReader(initialCatalog)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func importCatalog(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM catalog"); e != nil {
		return e
	}
	if e = readCatalog(tx, f); e != nil {
		return e
	}
	return tx.Commit()
}
func readCatalog(tx *sql.Tx, source io.Reader) error {
	sc := bufio.NewScanner(source)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if !utf8.ValidString(s) || !valid(s, 200) {
			return fmt.Errorf("строка %d: требуется 1–200 символов UTF-8", line)
		}
		if _, e := tx.Exec("INSERT OR IGNORE INTO catalog(name,folded) VALUES(?,?)", s, strings.ToLower(s)); e != nil {
			return e
		}
	}
	return sc.Err()
}

type input struct {
	First, Last, Password, Name, Comment string
	Other                                bool
	ID, Taste, Looks                     int
}
type problem struct {
	status int
	msg    string
}

func (p problem) Error() string  { return p.msg }
func fail(n int, s string) error { return problem{n, s} }
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e != nil {
		p := problem{500, "Ошибка сервера. Попробуйте ещё раз."}
		if !errors.As(e, &p) {
			log.Print(e)
		}
		w.WriteHeader(p.status)
		v = map[string]string{"error": p.msg}
	}
	json.NewEncoder(w).Encode(v)
}

type action func(http.ResponseWriter, *http.Request, int, input) error

func post(route string, fn action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			respond(w, nil, fail(415, "Требуется JSON"))
			return
		}
		var i input
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		if d.Decode(&i) != nil || d.Decode(new(any)) != io.EOF {
			respond(w, nil, fail(400, "Некорректный запрос"))
			return
		}
		uid := 0
		if route != "register" && route != "login" {
			var e error
			uid, e = userID(r)
			if e != nil {
				respond(w, nil, e)
				return
			}
		}
		respond(w, map[string]bool{"ok": true}, fn(w, r, uid, i))
	}
}
func tokenHash(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }
func userID(r *http.Request) (int, error) {
	c, e := r.Cookie("byk_session")
	var id int
	if e == nil {
		e = db.QueryRow("SELECT uid FROM sessions WHERE token=? AND expires>?", tokenHash(c.Value), time.Now().Unix()).Scan(&id)
	}
	if e == http.ErrNoCookie || e == sql.ErrNoRows {
		return 0, fail(401, "Войдите в аккаунт")
	}
	return id, e
}
func newToken() string { b := make([]byte, 32); rand.Read(b); return hex.EncodeToString(b) }
func cookie(w http.ResponseWriter, t string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "byk_session", Value: t, Path: "/", MaxAge: age, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

type dish struct {
	ID   int
	Name string
}

func state(w http.ResponseWriter, r *http.Request) {
	uid, e := userID(r)
	if p, ok := e.(problem); ok && p.status == 401 {
		respond(w, nil, nil)
		return
	}
	if e != nil {
		respond(w, nil, e)
		return
	}
	var first, last string
	var created int64
	e = db.QueryRow("SELECT first,last,created FROM users WHERE id=?", uid).Scan(&first, &last, &created)
	if e != nil {
		respond(w, nil, e)
		return
	}
	rows, e := db.Query("SELECT id,name FROM dishes WHERE uid=? AND rated IS NULL ORDER BY id", uid)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	q := []dish{}
	for rows.Next() {
		var d dish
		if e = rows.Scan(&d.ID, &d.Name); e != nil {
			respond(w, nil, e)
			return
		}
		q = append(q, d)
	}
	respond(w, map[string]any{"first": first, "last": last, "created": created, "queue": q}, rows.Err())
}
func catalog(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, e := db.Query("SELECT name FROM catalog WHERE instr(folded,?)=1 ORDER BY folded LIMIT 12", q)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	a := []string{}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			respond(w, nil, e)
			return
		}
		a = append(a, s)
	}
	respond(w, a, rows.Err())
}
func add(w http.ResponseWriter, r *http.Request, id int, i input) error {
	name := strings.TrimSpace(i.Name)
	if i.Other && !valid(name, 50) {
		return fail(400, "Название должно содержать 1–50 символов")
	}
	if !i.Other {
		e := db.QueryRow("SELECT name FROM catalog WHERE folded=?", strings.ToLower(name)).Scan(&name)
		if e == sql.ErrNoRows {
			return fail(400, "Выберите блюдо из каталога или «Другое»")
		}
		if e != nil {
			return e
		}
	}
	res, e := db.Exec("INSERT INTO dishes(uid,name) SELECT ?,? WHERE (SELECT count(*) FROM dishes WHERE uid=? AND rated IS NULL)<100", id, name, id)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if n == 0 {
		return fail(409, "В списке уже 100 блюд")
	}
	return e
}
func change(query string, args ...any) error {
	res, e := db.Exec(query, args...)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if n == 0 {
		return fail(409, "Список уже изменился. Обновите страницу.")
	}
	return e
}
func removeDish(w http.ResponseWriter, r *http.Request, id int, i input) error {
	return change("DELETE FROM dishes WHERE uid=? AND id=? AND rated IS NULL", id, i.ID)
}
func validateReview(i input) error {
	if i.Taste < 1 || i.Taste > 10 || i.Looks < 1 || i.Looks > 10 || utf8.RuneCountInString(i.Comment) > 1000 {
		return fail(400, "Выберите две оценки от 1 до 10 и комментарий до 1000 символов")
	}
	return nil
}
func rate(w http.ResponseWriter, r *http.Request, id int, i input) error {
	if err := validateReview(i); err != nil {
		return err
	}
	return change("UPDATE dishes SET taste=?,looks=?,comment=?,rated=? WHERE uid=? AND id=? AND rated IS NULL", i.Taste, i.Looks, strings.TrimSpace(i.Comment), time.Now().Unix(), id, i.ID)
}
func editReview(w http.ResponseWriter, r *http.Request, id int, i input) error {
	if err := validateReview(i); err != nil {
		return err
	}
	return change("UPDATE dishes SET taste=?,looks=?,comment=? WHERE uid=? AND id=? AND rated IS NOT NULL", i.Taste, i.Looks, strings.TrimSpace(i.Comment), id, i.ID)
}
func deleteReview(w http.ResponseWriter, r *http.Request, id int, i input) error {
	return change("DELETE FROM dishes WHERE uid=? AND id=? AND rated IS NOT NULL", id, i.ID)
}

type review struct {
	ID, Taste, Looks      int
	Date                  int64
	Name, Author, Comment string
}

func reviews(w http.ResponseWriter, r *http.Request) {
	query := "SELECT d.id,d.name,d.taste,d.looks,d.comment,d.rated,u.first||' '||u.last FROM dishes d JOIN users u ON u.id=d.uid WHERE d.rated IS NOT NULL"
	args := []any{}
	if r.URL.Query().Get("mine") == "1" {
		id, e := userID(r)
		if e != nil {
			respond(w, nil, e)
			return
		}
		query += " AND uid=?"
		args = append(args, id)
	}
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		date, id, ok := strings.Cut(cursor, ",")
		ts, e := strconv.ParseInt(date, 10, 64)
		n, e2 := strconv.Atoi(id)
		if !ok || e != nil || e2 != nil {
			respond(w, nil, fail(400, "Неверная страница"))
			return
		}
		query += " AND (d.rated,d.id)<(?,?)"
		args = append(args, ts, n)
	}
	rows, e := db.Query(query+" ORDER BY d.rated DESC,d.id DESC LIMIT 30", args...)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	a := []review{}
	for rows.Next() {
		var v review
		if e = rows.Scan(&v.ID, &v.Name, &v.Taste, &v.Looks, &v.Comment, &v.Date, &v.Author); e != nil {
			respond(w, nil, e)
			return
		}
		a = append(a, v)
	}
	respond(w, a, rows.Err())
}
