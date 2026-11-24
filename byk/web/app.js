"use strict";
const app = document.querySelector("#app"),
  errorBox = document.querySelector("#error"),
  avatar = document.querySelector("#avatar");
let user,
  generation = 0;
const esc = (s) =>
  String(s).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const date = (ts) =>
  new Intl.DateTimeFormat("ru-RU", {
    timeZone: "Europe/Moscow",
    dateStyle: "long",
  }).format(ts * 1000);
const back = '<a class="back" href="#home">На главную</a>';
const publicLink = '<a class="foot" href="#public">Публичные оценки</a>';
async function api(path, body) {
  const response = await fetch(
    "/api/" + path,
    body === undefined
      ? {}
      : {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
  );
  const result = await response.json();
  if (!response.ok) throw Error(result.error || "Запрос не выполнен");
  return result;
}
function report(e) {
  errorBox.textContent = e.message || "Ошибка соединения";
  errorBox.scrollIntoView({ block: "nearest" });
}
function onForm(fn) {
  const f = app.querySelector("form");
  f.onsubmit = async (e) => {
    e.preventDefault();
    const b = f.querySelector("button[type=submit]");
    if (b.disabled) return;
    b.disabled = true;
    errorBox.textContent = "";
    try {
      await fn(Object.fromEntries(new FormData(f)));
    } catch (e) {
      report(e);
    } finally {
      b.disabled = false;
    }
  };
}
function go(page) {
  if (location.hash === "#" + page) render();
  else location.hash = page;
}
function auth(login) {
  const title = login ? "Вход" : "Регистрация";
  app.innerHTML = `<nav class="tabs" aria-label="Аккаунт"><a href="#register" class="${login ? "" : "active"}">Регистрация</a><a href="#login" class="${login ? "active" : ""}">Вход</a></nav><h1>${title}</h1><form class="form"><label>Фамилия<input name="Last" autocomplete="family-name" required></label><label>Имя<input name="First" autocomplete="given-name" required></label><label>Пароль<input name="Password" type="password" required autocomplete="${login ? "current-password" : "new-password"}" aria-describedby="hint"></label><small id="hint">От 3 до 30 символов</small><button type="submit" class="primary">${login ? "Войти" : "Создать аккаунт"}</button></form>${publicLink}`;
  onForm(async (data) => {
    await api(login ? "login" : "register", data);
    go("home");
  });
}
function home() {
  app.innerHTML =
    '<div class="home"><a class="btn primary" href="#add"><span aria-hidden="true" class="symbol">+</span>Добавить блюдо</a><a class="btn" href="#rate"><span aria-hidden="true" class="symbol">☆</span>Оценить блюдо</a></div>';
}
function add() {
  app.innerHTML =
    back +
    `<h1>Добавить блюдо</h1><form class="form"><label>Поиск по каталогу<input id="query" autocomplete="off" placeholder="Начните вводить название"></label><div id="choices" class="list" aria-label="Подсказки"></div><button type="button" id="other">Другое</button><label id="custom" hidden>Название блюда<input name="Name" placeholder="От 1 до 50 символов"></label><button type="submit" class="primary">Добавить в список</button></form><h2>Текущий визит · ${user.queue.length}</h2><div id="queue"></div><a class="foot" href="#rate">Перейти к оценке</a>`;
  const q = app.querySelector("#query"),
    choices = app.querySelector("#choices"),
    custom = app.querySelector("#custom"),
    field = custom.querySelector("input");
  let chosen = "",
    other = false,
    seq = 0;
  async function search() {
    const n = ++seq;
    chosen = "";
    other = false;
    custom.hidden = true;
    field.required = false;
    choices.replaceChildren();
    const names = await api("catalog?q=" + encodeURIComponent(q.value));
    if (n !== seq) return;
    if (!names.length)
      choices.textContent = "Совпадений нет. Можно выбрать «Другое».";
    for (const name of names) {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = name;
      b.onclick = () => {
        seq++;
        chosen = name;
        q.value = name;
        choices.replaceChildren();
        app.querySelector("[type=submit]").focus();
      };
      choices.append(b);
    }
  }
  q.oninput = () => search().catch(report);
  q.onfocus = () => {
    if (!chosen) search().catch(report);
  };
  app.querySelector("#other").onclick = () => {
    seq++;
    other = true;
    chosen = "";
    field.value = q.value;
    choices.replaceChildren();
    q.value = "";
    custom.hidden = false;
    field.required = true;
    field.focus();
  };
  onForm(async (data) => {
    if (!other && !chosen) throw Error("Выберите подсказку или «Другое»");
    await api("add", { Name: other ? data.Name : chosen, Other: other });
    await render();
    app.querySelector("#query")?.focus();
  });
  const queue = app.querySelector("#queue");
  if (!user.queue.length) queue.textContent = "Список пока пуст.";
  user.queue.forEach((d) => {
    const row = document.createElement("div");
    row.className = "row";
    row.innerHTML = `<span>${esc(d.Name)}</span><button class="quiet" aria-label="Убрать ${esc(d.Name)}">Убрать</button>`;
    row.querySelector("button").onclick = async () => {
      try {
        await api("remove", { ID: d.ID });
        await render();
      } catch (e) {
        report(e);
      }
    };
    queue.append(row);
  });
}
function scale(name, title) {
  return `<fieldset class="score"><legend>${title}</legend><div class="scale">${Array.from({ length: 10 }, (_, i) => `<label><input type="radio" name="${name}" value="${i + 1}" required>${i + 1}</label>`).join("")}</div></fieldset>`;
}
function rate(existing) {
  const d = existing || user.queue[0];
  if (!d) {
    app.innerHTML =
      back +
      '<h1>Список пуст</h1><p>Добавьте блюда текущего визита, чтобы оценить их по очереди.</p><a class="btn primary" href="#add">Добавить блюдо</a>';
    return;
  }
  app.innerHTML =
    (existing ? '<button class="quiet" id="cancel-edit">Отмена</button><p class="muted">Изменение оценки</p>' : back + `<p class="muted">Осталось оценить · ${user.queue.length}</p>`) +
    `<h1>${esc(d.Name)}</h1><form class="form">${scale("Taste", "Вкус")}${scale("Looks", "Внешний вид")}<label>Комментарий <small>· по желанию</small><textarea name="Comment" rows="3"></textarea></label><small>Оценки и комментарий будут видны всем под вашим именем.</small><button class="primary" type="submit">${existing ? "Сохранить изменения" : user.queue.length === 1 ? "Завершить" : "Продолжить"}</button></form>`;
  if (existing) {
    const fields = app.querySelector("form").elements;
    fields.Taste.value = existing.Taste;
    fields.Looks.value = existing.Looks;
    fields.Comment.value = existing.Comment;
    app.querySelector("#cancel-edit").onclick = render;
    app.focus();
    window.scrollTo(0, 0);
  }
  onForm(async (v) => {
    await api(existing ? "reviews/edit" : "rate", {
      ID: d.ID,
      Taste: +v.Taste,
      Looks: +v.Looks,
      Comment: v.Comment,
    });
    if (existing || user.queue.length === 1) go("profile");
    else await render();
  });
}
async function feed(mine, g) {
  app.innerHTML =
    (user ? back : '<a class="back" href="#login">Войти</a>') +
    (mine
      ? `<h1>${esc(user.first)} ${esc(user.last)}</h1><p>Аккаунт создан ${date(user.created)}</p>${publicLink}<h2>Мои оценки</h2>`
      : "<h1>Публичные оценки</h1>") +
    '<div id="feed"></div><button hidden id="more">Показать ещё</button>';
  const target = app.querySelector("#feed"),
    more = app.querySelector("#more");
  let before = "",
    previous = "";
  async function load() {
    more.disabled = true;
    try {
      const rows = await api(
        "reviews?mine=" +
          (mine ? "1" : "0") +
          "&before=" +
          encodeURIComponent(before),
      );
      if (g !== generation) return;
      if (!rows.length && !before) target.textContent = "Пока нет оценок.";
      for (const r of rows) {
        const day = date(r.Date);
        if (day !== previous) {
          target.insertAdjacentHTML(
            "beforeend",
            `<h3 class="date">${day}</h3>`,
          );
          previous = day;
        }
        target.insertAdjacentHTML(
          "beforeend",
          `<article class="review"><h3>${esc(r.Name)}</h3>${mine ? "" : `<small>${esc(r.Author)}</small>`}<p class="metrics"><span>Вкус <b>${r.Taste}/10</b></span><span>Внешний вид <b>${r.Looks}/10</b></span></p>${r.Comment ? `<p class="muted review-comment">${esc(r.Comment)}</p>` : ""}</article>`,
        );
        if (mine) {
          const controls = document.createElement("div");
          controls.className = "actions";
          controls.innerHTML = '<button type="button">Изменить</button><button type="button" class="quiet">Удалить</button>';
          target.lastElementChild.append(controls);
          controls.firstElementChild.onclick = () => rate(r);
          controls.lastElementChild.onclick = async () => {
            if (!window.confirm(`Удалить вашу оценку блюда «${r.Name}»?`)) return;
            try {
              await api("reviews/delete", { ID: r.ID });
              await render();
            } catch (e) { report(e); }
          };
        }
        before = r.Date + "," + r.ID;
      }
      more.hidden = rows.length < 30;
    } finally {
      more.disabled = false;
    }
  }
  more.onclick = () => load().catch(report);
  if (mine) {
    app.insertAdjacentHTML(
      "beforeend",
      '<div class="actions"><button id="logout">Выйти</button><a class="btn quiet" href="#delete">Удалить аккаунт</a></div>',
    );
    app.querySelector("#logout").onclick = async () => {
      try {
        await api("logout", {});
        go("login");
      } catch (e) {
        report(e);
      }
    };
  }
  await load();
}
async function render() {
  const g = ++generation;
  errorBox.textContent = "";
  try {
    const state = await api("state");
    if (g !== generation) return;
    user = state;
    avatar.hidden = !user;
    avatar.textContent = user
      ? (Array.from(user.first)[0] + Array.from(user.last)[0]).toLocaleUpperCase("ru")
      : "";
    const page = location.hash.slice(1);
    if (page === "public") await feed(false, g);
    else if (!user) auth(page === "login");
    else if (page === "profile") await feed(true, g);
    else if (page === "add") add();
    else if (page === "rate") rate();
    else if (page === "delete") {
      app.innerHTML =
        '<a class="back" href="#profile">В профиль</a><h1>Удалить аккаунт?</h1><p>Все ваши оценки и блюда текущего визита будут удалены без возможности восстановления.</p><form class="form"><label>Подтвердите пароль<input name="Password" type="password" required autocomplete="current-password"></label><button type="submit">Удалить навсегда</button></form>';
      onForm(async (v) => {
        await api("delete", v);
        go("register");
      });
    } else home();
    if (g === generation) {
      app.focus();
      window.scrollTo(0, 0);
    }
  } catch (e) {
    if (g === generation) report(e);
  }
}
window.addEventListener("hashchange", render);
render();
