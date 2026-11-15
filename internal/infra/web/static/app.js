// sms2telegram web UI. No dependencies: everything the page needs is in this
// file, since the browsers that open it often cannot reach a CDN.
"use strict";

const $ = (sel, root = document) => root.querySelector(sel);

// el builds a DOM node. Text always goes in as text, never as HTML: every
// message on this page was written by a stranger.
function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value === true ? "" : value);
  }
  node.append(...children.filter((c) => c !== null && c !== undefined && c !== false));
  return node;
}

const icon = (name) => {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "icon");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", "#i-" + name);
  svg.append(use);
  return svg;
};

const state = {
  user: null,
  conversations: [],
  peer: null, // the open conversation's peer
  draft: null, // {to, name}: a new conversation that has no message yet
  messages: [],
  query: "",
  status: null,
  events: null,
};

// --- API ---------------------------------------------------------------

class ApiError extends Error {
  constructor(status, message, body) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

async function api(path, body) {
  const options = body === undefined
    ? {}
    : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  const response = await fetch("api/" + path, { credentials: "same-origin", ...options });
  let data = null;
  try { data = await response.json(); } catch { /* an empty or non-JSON answer */ }
  if (response.status === 401 && path !== "login") showLogin();
  if (!response.ok) throw new ApiError(response.status, (data && data.error) || response.statusText, data);
  return data;
}

// --- formatting --------------------------------------------------------

const timeFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat(undefined, { weekday: "long", day: "numeric", month: "long", year: "numeric" });
const shortFmt = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
const fullFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

const dayKey = (d) => d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate();

function dayLabel(d) {
  const today = new Date();
  const yesterday = new Date(Date.now() - 864e5);
  if (dayKey(d) === dayKey(today)) return "Today";
  if (dayKey(d) === dayKey(yesterday)) return "Yesterday";
  return dayFmt.format(d);
}

function listTime(d) {
  if (dayKey(d) === dayKey(new Date())) return timeFmt.format(d);
  return shortFmt.format(d);
}

function ago(iso) {
  const t = Date.parse(iso);
  if (!t || t < 0 || new Date(t).getFullYear() < 2000) return "never";
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 90) return s + " s ago";
  if (s < 5400) return Math.round(s / 60) + " min ago";
  if (s < 129600) return Math.round(s / 3600) + " h ago";
  return Math.round(s / 86400) + " d ago";
}

function duration(seconds) {
  if (!seconds) return "unknown";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h ? h + " h " + m + " min" : m + " min";
}

const title = (m) => m.name || m.number || "Unknown sender";

function initial(text) {
  const ch = [...text.replace(/^[+\s]+/, "")][0] || "?";
  return /\d/.test(ch) ? "#" : ch.toUpperCase();
}

function colorOf(text) {
  let sum = 0;
  for (const ch of text) sum = (sum + ch.codePointAt(0)) % 6;
  return sum ? "c" + sum : "";
}

// The GSM 03.38 alphabet, to count a message the way the network bills it.
const GSM = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ ÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
  "¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà";
const GSM_EXT = "\f^{}\\[~]|€";

function measure(text) {
  let septets = 0;
  let gsm = true;
  for (const ch of text) {
    if (GSM.includes(ch)) septets += 1;
    else if (GSM_EXT.includes(ch)) septets += 2;
    else { gsm = false; break; }
  }
  const units = gsm ? septets : text.length; // .length counts UTF-16 units
  const [single, part] = gsm ? [160, 153] : [70, 67];
  const parts = units <= single ? 1 : Math.ceil(units / part);
  return { encoding: gsm ? "GSM-7" : "UCS-2", units, parts, room: (parts === 1 ? single : parts * part) - units };
}

// body renders a message's text with links and tap-to-copy codes.
const TOKEN = /(https?:\/\/[^\s<>"']+)|((?<![\p{L}\p{N}]|[\d۰-۹][.,/:-])[\d۰-۹]{4,8}(?![\p{L}\p{N}]|[.,/:-][\d۰-۹]))/gu;

function body(text) {
  const out = el("div", { class: "body", dir: "auto" });
  let last = 0;
  for (const match of text.matchAll(TOKEN)) {
    out.append(text.slice(last, match.index));
    if (match[1]) {
      out.append(el("a", { href: match[1], target: "_blank", rel: "noopener noreferrer" }, match[1]));
    } else {
      out.append(el("button", { class: "code", type: "button", title: "Copy", onclick: () => copy(match[2]) }, match[2]));
    }
    last = match.index + match[0].length;
  }
  out.append(text.slice(last));
  return out;
}

async function copy(text) {
  const latin = text.replace(/[۰-۹]/g, (d) => String(d.charCodeAt(0) - 0x6f0));
  try {
    await navigator.clipboard.writeText(latin);
    toast("Copied " + latin);
  } catch {
    toast("Copy is blocked on this page", true);
  }
}

let toastTimer = 0;
function toast(text, bad = false) {
  const node = $("#toast");
  node.textContent = text;
  node.className = "toast" + (bad ? " bad" : "");
  node.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { node.hidden = true; }, bad ? 6000 : 2500);
}

// --- login -------------------------------------------------------------

function showLogin() {
  if (state.events) { state.events.close(); state.events = null; }
  state.user = null;
  $("#app").hidden = true;
  $("#login").hidden = false;
  $("#login-form").username.focus();
}

$("#login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const error = $("#login-error");
  error.hidden = true;
  try {
    await api("login", { username: form.username.value, password: form.password.value });
    form.password.value = "";
    await start();
  } catch (err) {
    error.textContent = err.message;
    error.hidden = false;
  }
});

$("#logout").addEventListener("click", async () => {
  try { await api("logout", {}); } catch { /* the session is gone either way */ }
  showLogin();
});

// --- conversations -----------------------------------------------------

async function loadConversations() {
  state.conversations = await api("conversations");
  renderList();
}

function renderList() {
  const list = $("#list");
  list.replaceChildren();

  if (state.query) return; // the search results are showing

  if (state.draft) {
    list.append(item({ peer: null, name: state.draft.name, last: { number: state.draft.to, content: "New message", time: new Date().toISOString(), direction: "out" } }, true));
  }

  for (const c of state.conversations) list.append(item(c, !state.draft && c.peer === state.peer));

  if (!list.children.length) list.append(el("p", { class: "note" }, "No messages yet. They appear here as they arrive."));
}

function item(c, current, onclick) {
  const name = c.name || c.last.number || "Unknown sender";
  return el("button", {
    class: "item", type: "button", "aria-current": current ? "true" : null,
    onclick: onclick || (() => c.peer && openPeer(c.peer)),
  },
  el("span", { class: "avatar " + colorOf(name) }, initial(name)),
  el("span", { class: "grow" },
    el("span", { class: "line1" }, el("strong", { dir: "auto" }, name), el("time", {}, listTime(new Date(c.last.time)))),
    el("span", { class: "snippet", dir: "auto" },
      c.last.direction === "out" ? el("span", { class: "me" }, "You: ") : null, c.last.content.slice(0, 120))));
}

async function openPeer(peer) {
  state.peer = peer;
  state.draft = null;
  state.messages = await api("messages?peer=" + encodeURIComponent(peer));
  renderList();
  renderThread();
  $("#app").classList.add("reading");
}

function openDraft(to, name) {
  state.peer = null;
  state.draft = { to, name: name || "" };
  state.messages = [];
  renderList();
  renderThread();
  $("#app").classList.add("reading");
  $("#text").focus();
}

// recipient is the number a reply in the open conversation goes to.
function recipient() {
  if (state.draft) return state.draft.to;
  const last = state.messages[state.messages.length - 1];
  return last && /^\+?\d{3,15}$/.test(last.number) ? last.number : "";
}

function renderThread() {
  const open = state.peer !== null || state.draft !== null;
  $("#empty").hidden = open;
  $("#conv").hidden = !open;
  if (!open) return;

  const last = state.messages[state.messages.length - 1];
  const name = state.draft ? (state.draft.name || state.draft.to) : title(last || {});
  const number = state.draft ? state.draft.to : (last ? last.number : "");

  $("#conv-name").textContent = name;
  $("#conv-sub").textContent = name === number ? state.messages.length + (state.messages.length === 1 ? " message" : " messages") : number;
  const avatar = $("#conv-avatar");
  avatar.textContent = initial(name);
  avatar.className = "avatar " + colorOf(name);

  const box = $("#messages");
  box.replaceChildren();
  let day = "";
  for (const m of state.messages) {
    const at = new Date(m.time);
    if (dayKey(at) !== day) {
      day = dayKey(at);
      box.append(el("div", { class: "day" }, dayLabel(at)));
    }
    box.append(bubble(m));
  }
  box.scrollTop = box.scrollHeight;

  const canReply = recipient() !== "";
  $("#composer").hidden = !canReply;
  $("#readonly").hidden = canReply;
  count();
}

function bubble(m, showWho = false) {
  const out = m.direction === "out";
  const failed = out && m.status === "failed";
  return el("div", { class: "msg" + (out ? " out" : "") + (failed ? " failed" : "") + (m.pending ? " pending" : ""), title: fullFmt.format(new Date(m.time)) },
    showWho ? el("div", { class: "who", dir: "auto" }, (out ? "To " : "") + title(m)) : null,
    body(m.content),
    el("div", { class: "meta" },
      failed ? el("span", { class: "fail", title: m.error || "" }, "not confirmed") : null,
      m.pending ? el("span", {}, "sending…") : null,
      out && m.source && m.source !== "ui" ? el("span", { class: "tag" }, m.source) : null,
      el("time", {}, timeFmt.format(new Date(m.time)))));
}

// --- sending -----------------------------------------------------------

const text = $("#text");

function count() {
  const counter = $("#counter");
  text.style.height = "auto";
  text.style.height = Math.min(text.scrollHeight + 2, 152) + "px";
  if (!text.value) { counter.textContent = ""; return; }
  const size = measure(text.value);
  counter.textContent = size.room + " left · " + size.parts + (size.parts === 1 ? " SMS · " : " SMS, each billed · ") + size.encoding;
  counter.className = "counter mono" + (size.parts > 3 ? " bad" : size.parts > 1 ? " warn" : "");
}

text.addEventListener("input", count);
text.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    $("#composer").requestSubmit();
  }
});

$("#composer").addEventListener("submit", async (event) => {
  event.preventDefault();
  const to = recipient();
  const content = text.value;
  if (!to || !content.trim()) return;

  const pending = { direction: "out", number: to, content, time: new Date().toISOString(), pending: true };
  state.messages.push(pending);
  text.value = "";
  renderThread();

  let sent = null;
  try {
    sent = await api("send", { to, text: content });
  } catch (err) {
    sent = err.body && err.body.message;
    toast(err.message, true);
    if (!sent) text.value = content; // refused before it was tried: give the text back
  }

  state.messages = state.messages.filter((m) => m !== pending);
  if (sent) {
    if (!state.messages.some((m) => m.id === sent.id)) state.messages.push(sent);
    state.peer = sent.peer;
    state.draft = null;
  }
  renderThread();
  await Promise.all([loadConversations(), loadStatus()]);
});

// --- live updates ------------------------------------------------------

function connect() {
  if (state.events) state.events.close();
  const events = new EventSource("api/events");
  state.events = events;
  events.addEventListener("message", (event) => {
    const m = JSON.parse(event.data);
    if (m.peer === state.peer && !state.messages.some((x) => x.id === m.id)) {
      // A message sent from this page arrives here first when the modem is
      // quick; the pending bubble is replaced when the request answers.
      if (m.direction === "in" || !state.messages.some((x) => x.pending)) {
        state.messages.push(m);
        renderThread();
      }
    }
    if (m.direction === "in" && document.hidden) document.title = "● sms2telegram";
    loadConversations().catch(() => {});
  });
  // EventSource reconnects by itself; what arrived meanwhile is fetched again.
  events.addEventListener("open", () => { loadConversations().catch(() => {}); });
}

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) document.title = "sms2telegram";
});

// --- search ------------------------------------------------------------

let searchTimer = 0;
$("#search").addEventListener("input", (event) => {
  clearTimeout(searchTimer);
  const query = event.currentTarget.value.trim();
  searchTimer = setTimeout(() => search(query), 180);
});

async function search(query) {
  state.query = query;
  if (!query) { renderList(); return; }
  const found = await api("messages?limit=60&q=" + encodeURIComponent(query));
  if (state.query !== query) return; // a newer search is on its way
  const list = $("#list");
  list.replaceChildren();
  for (const m of found.reverse()) {
    list.append(item({ peer: m.peer, name: m.name, last: m }, false, async () => { await openPeer(m.peer); }));
  }
  if (!found.length) list.append(el("p", { class: "note" }, "Nothing matches “" + query + "”."));
}

// --- status ------------------------------------------------------------

async function loadStatus() {
  const s = await api("status");
  state.status = s;

  const level = !s.modem_ok ? 0 : s.signal_dbm >= -85 ? 4 : s.signal_dbm >= -95 ? 3 : s.signal_dbm >= -105 ? 2 : 1;
  [...$("#bars").children].forEach((bar, i) => bar.classList.toggle("on", i < level));
  const online = s.modem_ok && s.sim && s.registered;
  $("#net-text").textContent = !s.modem_ok ? "Modem unreachable" : !s.sim ? "No SIM" : !s.registered ? "No network" : s.network.replace(/\s+/g, " ").trim();
  $("#pill-net").className = "pill " + (online ? "ok" : "bad");

  $("#pill-beat").hidden = !s.heartbeat;
  $("#beat-text").textContent = s.heartbeat_missing ? "Heartbeat missing" : ago(s.heartbeat_received);
  $("#pill-beat").className = "pill " + (s.heartbeat_missing ? "bad" : "ok");

  $("#pill-quota").hidden = s.daily_limit <= 0;
  $("#quota-text").textContent = s.remaining + " of " + s.daily_limit + " left";
  $("#pill-quota").className = "pill " + (s.remaining === 0 ? "bad" : s.remaining <= 5 ? "warn" : "");

  $("#open-ussd").hidden = !s.ussd;
  if ($("#status-dialog").open) renderStatus();
}

function renderStatus() {
  const s = state.status;
  if (!s) return;
  const fact = (name, value, cls) => [el("dt", {}, name), el("dd", { class: cls || null }, value)];
  const rows = [
    fact("Modem", s.modem_ok ? "answering" : "unreachable", s.modem_ok ? "ok" : "bad"),
    fact("SIM", s.sim ? "present" : "missing", s.sim ? "ok" : "bad"),
    fact("Network", s.registered ? s.network.replace(/\s+/g, " ").trim() : "not registered", s.registered ? "ok" : "bad"),
    fact("Signal", s.signal_dbm ? s.signal_dbm + " dBm" : "unknown"),
    fact("Modem uptime", duration(s.modem_uptime_seconds)),
    fact("Inbox read", ago(s.inbox_read)),
    fact("Telegram delivery", ago(s.delivered), s.notices ? "warn" : null),
    fact("On the modem", s.inbox + " messages"),
    fact("In the history", s.archive + " messages"),
    fact("Forwarded since start", String(s.forwarded)),
  ];
  if (s.heartbeat) {
    rows.push(fact("Heartbeat sent", ago(s.heartbeat_sent)),
      fact("Heartbeat received", s.heartbeat_missing ? "missing" : ago(s.heartbeat_received), s.heartbeat_missing ? "bad" : "ok"));
  }
  if (s.notices) rows.push(fact("Alerts waiting for Telegram", String(s.notices), "warn"));
  rows.push(fact("Names known", s.contacts + " numbers"),
    fact("Sending limit", s.daily_limit > 0 ? s.remaining + " of " + s.daily_limit + " left today" : "none"),
    fact("Version", s.version));
  $("#status-list").replaceChildren(...rows.flat());
}

$("#status").addEventListener("click", () => { renderStatus(); $("#status-dialog").showModal(); });

// --- dialogs -----------------------------------------------------------

for (const button of document.querySelectorAll("[data-close]")) {
  button.addEventListener("click", () => button.closest("dialog").close());
}

function openNew() {
  $("#new-to").value = "";
  $("#suggest").replaceChildren();
  $("#new-dialog").showModal();
}

$("#new").addEventListener("click", openNew);

let suggestTimer = 0;
$("#new-to").addEventListener("input", (event) => {
  clearTimeout(suggestTimer);
  const query = event.currentTarget.value.trim();
  suggestTimer = setTimeout(async () => {
    const found = query.length < 2 ? [] : await api("contacts?q=" + encodeURIComponent(query));
    $("#suggest").replaceChildren(...found.map((c) => el("button", { type: "button", onclick: () => startWith(c.number, c.name) },
      el("span", { dir: "auto" }, c.name), el("span", { class: "mono muted" }, c.number))));
  }, 150);
});

async function startWith(to, name) {
  try {
    // A preview with a placeholder text checks the number and normalizes it.
    const preview = await api("preview", { to, text: "." });
    $("#new-dialog").close();
    const known = state.conversations.find((c) => c.peer === preview.to);
    if (known) await openPeer(known.peer);
    else openDraft(preview.to, name || preview.name);
  } catch (err) {
    toast(err.message, true);
  }
}

$("#new-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const first = $("#suggest button");
  const typed = $("#new-to").value.trim();
  if (!/\d/.test(typed) && first) first.click();
  else startWith(typed, "");
});

$("#open-ussd").addEventListener("click", () => { $("#ussd-dialog").showModal(); $("#ussd-code").focus(); });

$("#ussd-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = $("#ussd-code");
  const code = input.value.trim();
  const log = $("#ussd-log");
  const button = event.currentTarget.querySelector("button[type=submit]");
  log.append(el("div", { class: "ask" }, code));
  const wait = el("div", { class: "muted" }, "Waiting for the network…");
  log.append(wait);
  log.scrollTop = log.scrollHeight;
  input.value = "";
  button.disabled = true;
  try {
    const { answer } = await api("ussd", { code });
    wait.replaceWith(el("div", { dir: "auto" }, answer));
  } catch (err) {
    wait.replaceWith(el("div", { class: "err" }, err.message));
  }
  button.disabled = false;
  log.scrollTop = log.scrollHeight;
  input.focus();
});

$("#back").addEventListener("click", () => { $("#app").classList.remove("reading"); });

document.addEventListener("keydown", (event) => {
  if (event.target.matches("input, textarea") || document.querySelector("dialog[open]") || !state.user) return;
  if (event.key === "/") { event.preventDefault(); $("#search").focus(); }
  if (event.key === "n") { event.preventDefault(); openNew(); }
});

// --- start -------------------------------------------------------------

async function start() {
  const me = await api("me");
  state.user = me.user;
  $("#who").textContent = me.user;
  $("#login").hidden = true;
  $("#app").hidden = false;
  await Promise.all([loadConversations(), loadStatus()]);
  connect();
}

setInterval(() => { if (state.user && !document.hidden) loadStatus().catch(() => {}); }, 15000);

start().catch((err) => { if (err.status !== 401) toast(err.message, true); });
