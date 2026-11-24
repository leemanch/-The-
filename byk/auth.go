package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const lifetime = 30 * 24 * 3600

var hashSlots = make(chan struct{}, 2)

func password(s string, salt []byte) ([]byte, error) {
	// Bound CPU work when several friends sign in together.
	select {
	case hashSlots <- struct{}{}:
		defer func() { <-hashSlots }()
	case <-time.After(5 * time.Second):
		return nil, fail(429, "Много запросов. Попробуйте ещё раз.")
	}
	return pbkdf2.Key(sha256.New, s, salt, 600000, 32)
}
func identity(i input) string {
	return strings.ToLower(strings.TrimSpace(i.Last)) + "\x00" + strings.ToLower(strings.TrimSpace(i.First))
}
func credentials(i input) error {
	n := utf8.RuneCountInString(i.Password)
	if n < 3 || n > 30 || !valid(strings.TrimSpace(i.First), 50) || !valid(strings.TrimSpace(i.Last), 50) || strings.ContainsRune(i.First+i.Last, 0) {
		return fail(400, "Введите имя и фамилию до 50 символов и пароль от 3 до 30 символов")
	}
	return nil
}
func session(tx *sql.Tx, id int) (string, error) {
	t := newToken()
	now := time.Now().Unix()
	if _, e := tx.Exec("DELETE FROM sessions WHERE expires<=?", now); e != nil {
		return "", e
	}
	_, e := tx.Exec("INSERT INTO sessions VALUES(?,?,?)", tokenHash(t), id, now+lifetime)
	return t, e
}
func finish(w http.ResponseWriter, tx *sql.Tx, id int) error {
	t, e := session(tx, id)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	cookie(w, t, lifetime)
	return nil
}
func register(w http.ResponseWriter, r *http.Request, id int, i input) error {
	if e := credentials(i); e != nil {
		return e
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	hash, e := password(i.Password, salt)
	if e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("INSERT INTO users(first,last,identity,salt,hash,created) VALUES(?,?,?,?,?,?) ON CONFLICT(identity) DO NOTHING", strings.TrimSpace(i.First), strings.TrimSpace(i.Last), identity(i), salt, hash, time.Now().Unix())
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return fail(409, "Такое имя и фамилия уже зарегистрированы")
	}
	uid, e := res.LastInsertId()
	if e != nil {
		return e
	}
	return finish(w, tx, int(uid))
}
func login(w http.ResponseWriter, r *http.Request, id int, i input) error {
	if e := credentials(i); e != nil {
		return e
	}
	var salt, hash []byte
	e := db.QueryRow("SELECT id,salt,hash FROM users WHERE identity=?", identity(i)).Scan(&id, &salt, &hash)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == sql.ErrNoRows {
		salt = make([]byte, 16)
	}
	actual, e := password(i.Password, salt)
	if e != nil {
		return e
	}
	if subtle.ConstantTimeCompare(actual, hash) != 1 {
		return fail(401, "Проверьте имя, фамилию и пароль")
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	return finish(w, tx, id)
}
func logout(w http.ResponseWriter, r *http.Request, id int, i input) error {
	c, _ := r.Cookie("byk_session")
	_, e := db.Exec("DELETE FROM sessions WHERE token=?", tokenHash(c.Value))
	if e == nil {
		cookie(w, "", -1)
	}
	return e
}
func removeUser(w http.ResponseWriter, r *http.Request, id int, i input) error {
	var salt, hash []byte
	if e := db.QueryRow("SELECT salt,hash FROM users WHERE id=?", id).Scan(&salt, &hash); e != nil {
		return e
	}
	actual, e := password(i.Password, salt)
	if e != nil {
		return e
	}
	if subtle.ConstantTimeCompare(actual, hash) != 1 {
		return fail(403, "Неверный пароль")
	}
	e = change("DELETE FROM users WHERE id=? AND hash=?", id, hash)
	if e == nil {
		cookie(w, "", -1)
	}
	return e
}
