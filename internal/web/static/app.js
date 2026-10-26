"use strict";

// AskYourBook chat UI. Talks to the JSON API on the same origin:
//   GET/POST /documents, GET/DELETE /documents/{id}, POST /documents/{id}/chat/stream

const $ = (id) => document.getElementById(id);
const el = {
  app: $("app"), docList: $("doc-list"), emptyDocs: $("empty-docs"),
  uploadBtn: $("upload-btn"), fileInput: $("file-input"),
  title: $("title"), newChat: $("new-chat"), menuBtn: $("menu-btn"),
  messages: $("messages"), composer: $("composer"), question: $("question"), send: $("send"),
  dropOverlay: $("drop-overlay"), toast: $("toast"),
};

const POLL_MS = 1500;
const MAX_STORED_MESSAGES = 100;
const SUGGESTIONS = [
  "Summarize this document",
  "What are the main topics covered?",
  "Explain the most important idea with an example",
];

const state = {
  docs: [],
  currentId: load("ayb:selected"),
  convs: {},        // docId -> {sessionId, messages: [...]}, mirrored to localStorage
  streaming: null,  // docId of the conversation currently receiving an answer
  pollTimer: null,
};

// ---------- Storage (per-browser conveniences; tolerate it being unavailable) ----------

function load(key) {
  try { return JSON.parse(localStorage.getItem(key)); } catch { return null; }
}

function save(key, value) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
  } catch { /* private mode or quota: the UI still works without it */ }
}

function newSessionId() {
  return crypto.randomUUID ? crypto.randomUUID()
    : Date.now().toString(36) + Math.random().toString(36).slice(2);
}

function conv(docId) {
  if (!state.convs[docId]) {
    const stored = load("ayb:chat:" + docId);
    state.convs[docId] = stored && Array.isArray(stored.messages)
      ? stored : { sessionId: newSessionId(), messages: [] };
  }
  return state.convs[docId];
}

function persist(docId) {
  const c = state.convs[docId];
  if (!c || !state.docs.some((d) => d.id === docId)) return; // deleted meanwhile
  const messages = c.messages.filter((m) => !m.pending).slice(-MAX_STORED_MESSAGES);
  save("ayb:chat:" + docId, { sessionId: c.sessionId, messages });
}

// ---------- API ----------

async function api(path, options = {}) {
  let res;
  try {
    res = await fetch(path, options);
  } catch {
    throw new Error("Couldn't reach the server. Is it running?");
  }
  if (!res.ok) throw new Error(await errorMessage(res));
  return res;
}

async function errorMessage(res) {
  try {
    const body = await res.json();
    if (body && body.error) return body.error;
  } catch { /* not JSON */ }
  return `Request failed (${res.status})`;
}

// ---------- Documents ----------

async function refreshDocs() {
  clearTimeout(state.pollTimer);
  try {
    const res = await api("/documents");
    state.docs = (await res.json()).documents || [];
  } catch (err) {
    showToast(err.message, true);
  }
  if (!state.docs.some((d) => d.id === state.currentId)) {
    const firstReady = state.docs.find((d) => d.status === "ready") || state.docs[0];
    state.currentId = firstReady ? firstReady.id : null;
    save("ayb:selected", state.currentId);
  }
  renderDocs();
  renderMain();
  if (state.docs.some((d) => d.status === "processing")) {
    state.pollTimer = setTimeout(refreshDocs, POLL_MS);
  }
}

function currentDoc() {
  return state.docs.find((d) => d.id === state.currentId) || null;
}

function selectDoc(id) {
  state.currentId = id;
  save("ayb:selected", id);
  el.app.classList.remove("drawer-open");
  renderDocs();
  renderMain();
  if (currentDoc()?.status === "ready") el.question.focus();
}

async function uploadFile(file) {
  if (!file) return;
  if (!/\.pdf$/i.test(file.name)) {
    showToast("Only PDF files are supported.", true);
    return;
  }
  const form = new FormData();
  form.append("file", file);
  showToast(`Uploading ${file.name}…`);
  try {
    const res = await api("/documents", { method: "POST", body: form });
    const up = await res.json();
    hideToast();
    state.docs.unshift({
      id: up.document_id, filename: up.filename, chunks: up.chunks,
      status: up.status, created_at: new Date().toISOString(),
    });
    selectDoc(up.document_id);
    refreshDocs(); // starts polling until it's ready
  } catch (err) {
    showToast(err.message, true);
  }
}

async function deleteDoc(doc) {
  if (!confirm(`Delete "${doc.filename}" and its chat?`)) return;
  try {
    await api("/documents/" + doc.id, { method: "DELETE" });
    delete state.convs[doc.id];
    save("ayb:chat:" + doc.id, null);
    if (state.currentId === doc.id) state.currentId = null;
    await refreshDocs();
  } catch (err) {
    showToast(err.message, true);
  }
}

function renderDocs() {
  el.docList.replaceChildren(...state.docs.map((doc) => {
    const li = document.createElement("li");
    li.className = "doc" + (doc.id === state.currentId ? " active" : "");
    li.tabIndex = 0;
    li.title = doc.filename;
    li.addEventListener("click", () => selectDoc(doc.id));
    li.addEventListener("keydown", (e) => { if (e.key === "Enter") selectDoc(doc.id); });

    const text = document.createElement("div");
    text.className = "doc-text";
    const name = document.createElement("div");
    name.className = "doc-name";
    name.textContent = doc.filename;
    const meta = document.createElement("div");
    meta.className = "doc-meta";
    const dot = document.createElement("span");
    dot.className = "dot " + doc.status;
    const label = document.createElement("span");
    label.textContent = {
      ready: `Ready · ${doc.chunks} chunks`,
      processing: "Indexing…",
      failed: "Failed",
    }[doc.status] || doc.status;
    meta.append(dot, label);
    text.append(name, meta);

    const del = document.createElement("button");
    del.className = "icon-btn delete-btn";
    del.type = "button";
    del.setAttribute("aria-label", `Delete ${doc.filename}`);
    del.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="M5 5l10 10M15 5 5 15" /></svg>';
    del.addEventListener("click", (e) => { e.stopPropagation(); deleteDoc(doc); });
    // Can't delete while it's indexing or while one of its answers streams.
    if (doc.status === "processing" || state.streaming === doc.id) del.disabled = true;

    li.append(text, del);
    return li;
  }));
  el.emptyDocs.hidden = state.docs.length > 0;
}

// ---------- Main panel ----------

function renderMain() {
  const doc = currentDoc();
  el.title.textContent = doc ? doc.filename : "AskYourBook";
  const ready = doc?.status === "ready";
  el.newChat.hidden = !ready || conv(doc.id).messages.length === 0;
  setComposer(ready && state.streaming === null,
    !doc ? "Upload a PDF to start"
      : doc.status === "processing" ? "Indexing… you can ask once it's ready"
      : doc.status === "failed" ? "This document failed to index"
      : "Ask a question about this document");

  if (!doc) return renderWelcome();
  if (doc.status === "processing") return renderStatus(true, `Indexing ${doc.filename}`,
    `Splitting into ${doc.chunks} chunks and embedding them. This usually takes a few seconds.`);
  if (doc.status === "failed") return renderStatus(false, "Indexing failed",
    `${doc.error || "Something went wrong"}. Delete it and try uploading again.`);

  const c = conv(doc.id);
  if (c.messages.length === 0) return renderSuggestions(doc);
  el.messages.replaceChildren(...c.messages.map(renderMessage));
  scrollToBottom();
}

function renderWelcome() {
  const box = document.createElement("div");
  box.className = "welcome";
  box.innerHTML = "<h1>Chat with your PDF</h1>" +
    "<p>Upload a PDF, then ask questions about it. Answers come only from the document and cite the pages they use.</p>";
  const btn = document.createElement("button");
  btn.className = "btn btn-primary";
  btn.type = "button";
  btn.textContent = "Upload a PDF";
  btn.addEventListener("click", () => el.fileInput.click());
  box.append(btn);
  el.messages.replaceChildren(box);
}

function renderStatus(spinning, heading, text) {
  const box = document.createElement("div");
  box.className = "welcome";
  if (spinning) {
    const s = document.createElement("div");
    s.className = "spinner";
    box.append(s);
  }
  const h = document.createElement("h1");
  h.textContent = heading;
  const p = document.createElement("p");
  p.textContent = text;
  box.append(h, p);
  el.messages.replaceChildren(box);
}

function renderSuggestions(doc) {
  const box = document.createElement("div");
  box.className = "welcome";
  const h = document.createElement("h1");
  h.textContent = "What would you like to know?";
  const p = document.createElement("p");
  p.textContent = `Ask anything about ${doc.filename}.`;
  const chips = document.createElement("div");
  chips.className = "suggestions";
  for (const s of SUGGESTIONS) {
    const chip = document.createElement("button");
    chip.className = "chip";
    chip.type = "button";
    chip.textContent = s;
    chip.addEventListener("click", () => ask(s));
    chips.append(chip);
  }
  box.append(h, p, chips);
  el.messages.replaceChildren(box);
}

function renderMessage(m) {
  const row = document.createElement("div");
  row.className = "msg " + m.role;
  const bubble = document.createElement("div");
  bubble.className = "bubble";
  row.append(bubble);

  if (m.role === "user") {
    bubble.textContent = m.text;
    return row;
  }
  if (m.error) {
    bubble.classList.add("error");
    bubble.textContent = m.error;
    return row;
  }
  if (m.pending && !m.text) {
    bubble.innerHTML = '<span class="typing" aria-label="Thinking"><span></span><span></span><span></span></span>';
    return row;
  }

  bubble.innerHTML = renderMarkdown(m.text);
  if (!m.pending && (m.sources?.length || m.rewritten)) bubble.append(renderMeta(m));
  bubble.addEventListener("click", (e) => {
    const cite = e.target.closest(".cite");
    if (cite) showSource(bubble, Number(cite.dataset.page));
  });
  return row;
}

function renderMeta(m) {
  const wrap = document.createElement("div");
  wrap.className = "meta";
  if (m.sources?.length) {
    const details = document.createElement("details");
    const summary = document.createElement("summary");
    const pages = [...new Set(m.sources.map((s) => s.page))].sort((a, b) => a - b);
    summary.textContent = `${m.sources.length} source${m.sources.length > 1 ? "s" : ""} · pages ${pages.join(", ")}`;
    const list = document.createElement("div");
    list.className = "sources";
    for (const s of m.sources) {
      const card = document.createElement("div");
      card.className = "source";
      card.dataset.page = s.page;
      const head = document.createElement("div");
      head.className = "source-head";
      const page = document.createElement("span");
      page.textContent = `Page ${s.page}`;
      const score = document.createElement("span");
      score.textContent = `${Math.round(s.score * 100)}% match`;
      head.append(page, score);
      const text = document.createElement("div");
      text.className = "source-text";
      text.textContent = s.snippet;
      card.append(head, text);
      list.append(card);
    }
    details.append(summary, list);
    wrap.append(details);
  }
  if (m.rewritten) {
    const q = document.createElement("div");
    q.className = "searched";
    q.textContent = `Searched for: “${m.rewritten}”`;
    wrap.append(q);
  }
  return wrap;
}

// showSource opens the message's sources and highlights the cited page.
function showSource(bubble, page) {
  const details = bubble.querySelector("details");
  const card = bubble.querySelector(`.source[data-page="${page}"]`);
  if (!details || !card) {
    showToast(`Page ${page} isn't among the passages retrieved for this answer.`);
    return;
  }
  details.open = true;
  card.scrollIntoView({ behavior: "smooth", block: "nearest" });
  card.classList.remove("flash");
  void card.offsetWidth; // restart the animation
  card.classList.add("flash");
  setTimeout(() => card.classList.remove("flash"), 1600);
}

function scrollToBottom() {
  el.messages.scrollTop = el.messages.scrollHeight;
}

// ---------- Asking ----------

async function ask(question) {
  question = question.trim();
  const doc = currentDoc();
  if (!question || !doc || doc.status !== "ready" || state.streaming) return;

  const docId = doc.id;
  const c = conv(docId);
  const answer = { role: "bot", text: "", pending: true };
  c.messages.push({ role: "user", text: question }, answer);
  state.streaming = docId;
  el.question.value = "";
  autosize();
  renderDocs();
  renderMain();

  // Re-render only the answer while it streams, and only if it's on screen.
  const update = () => {
    if (state.currentId !== docId) return;
    const last = el.messages.lastElementChild;
    if (last) last.replaceWith(renderMessage(answer));
    scrollToBottom();
  };

  try {
    const res = await api(`/documents/${docId}/chat/stream`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question, session_id: c.sessionId }),
    });
    await readEvents(res, (event, data) => {
      if (event === "token") {
        answer.text += data.text;
      } else if (event === "done") {
        answer.sources = data.sources || [];
        answer.rewritten = data.rewritten_query || "";
      } else if (event === "error") {
        answer.error = data.error || "The answer was interrupted.";
      }
      update();
    });
    if (!answer.text && !answer.error) answer.error = "No answer was returned.";
  } catch (err) {
    answer.error = err.message;
  }
  answer.pending = false;
  state.streaming = null;
  persist(docId);
  renderDocs();
  if (state.currentId === docId) renderMain();
  else setComposer(currentDoc()?.status === "ready", el.question.placeholder);
}

// readEvents parses a Server-Sent Events response body, calling onEvent
// with each event's name and JSON data.
async function readEvents(res, onEvent) {
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let end;
    while ((end = buf.indexOf("\n\n")) !== -1) {
      const block = buf.slice(0, end);
      buf = buf.slice(end + 2);
      let event = "message", data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event: ")) event = line.slice(7);
        else if (line.startsWith("data: ")) data += line.slice(6);
      }
      try { onEvent(event, JSON.parse(data)); } catch { /* ignore malformed events */ }
    }
  }
}

function setComposer(enabled, placeholder) {
  el.question.disabled = !enabled;
  el.question.placeholder = placeholder;
  el.send.disabled = !enabled || !el.question.value.trim();
}

function autosize() {
  el.question.style.height = "auto";
  el.question.style.height = Math.min(el.question.scrollHeight, 200) + "px";
}

// ---------- Markdown (small, safe subset) ----------

function escapeHTML(s) {
  return s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

// inline formats one escaped line: `code`, **bold**, *italic*, and page
// citations ("p. 12") as clickable chips.
function inline(text) {
  const parts = escapeHTML(text).split(/(`[^`]+`)/g);
  return parts.map((part) => {
    if (part.length > 1 && part.startsWith("`") && part.endsWith("`")) {
      return "<code>" + part.slice(1, -1) + "</code>";
    }
    return part
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>")
      // Keep a parenthesised group like "(p. 3, p. 7)" on one line.
      .replace(/\(((?:p\.\s?\d{1,4}(?:,\s*)?)+)\)/g, '<span class="cites">($1)</span>')
      .replace(/\bp\.\s?(\d{1,4})\b/g, '<button type="button" class="cite" data-page="$1">p. $1</button>');
  }).join("");
}

// renderMarkdown turns the model's answer into HTML. All text is escaped
// before any markup is added, so document content can't inject HTML.
function renderMarkdown(src) {
  const lines = src.replace(/\r\n/g, "\n").split("\n");
  const out = [];
  let para = [], list = null, code = null;

  const flushPara = () => {
    if (para.length) out.push("<p>" + para.map(inline).join("<br>") + "</p>");
    para = [];
  };
  const flushList = () => {
    if (list) out.push(`<${list.tag}>` + list.items.map((i) => "<li>" + inline(i) + "</li>").join("") + `</${list.tag}>`);
    list = null;
  };

  for (const line of lines) {
    if (code !== null) {
      if (line.trim().startsWith("```")) {
        out.push("<pre><code>" + escapeHTML(code.join("\n")) + "</code></pre>");
        code = null;
      } else {
        code.push(line);
      }
      continue;
    }
    if (line.trim().startsWith("```")) {
      flushPara(); flushList();
      code = [];
      continue;
    }
    const bullet = line.match(/^\s*[*-]\s+(.*)$/);
    const numbered = line.match(/^\s*\d+[.)]\s+(.*)$/);
    if (bullet || numbered) {
      flushPara();
      const tag = bullet ? "ul" : "ol";
      if (list && list.tag !== tag) flushList();
      if (!list) list = { tag, items: [] };
      list.items.push((bullet || numbered)[1]);
      continue;
    }
    if (!line.trim()) {
      flushPara(); flushList();
      continue;
    }
    flushList();
    const heading = line.match(/^#{1,6}\s+(.*)$/);
    para.push(heading ? "**" + heading[1] + "**" : line);
  }
  if (code !== null) out.push("<pre><code>" + escapeHTML(code.join("\n")) + "</code></pre>");
  flushPara(); flushList();
  return out.join("");
}

// ---------- Toast ----------

let toastTimer;
function showToast(message, isError = false) {
  clearTimeout(toastTimer);
  el.toast.textContent = message;
  el.toast.classList.toggle("error", isError);
  el.toast.hidden = false;
  toastTimer = setTimeout(hideToast, isError ? 6000 : 3500);
}

function hideToast() {
  el.toast.hidden = true;
}

// ---------- Events ----------

el.uploadBtn.addEventListener("click", () => el.fileInput.click());
el.fileInput.addEventListener("change", () => {
  uploadFile(el.fileInput.files[0]);
  el.fileInput.value = "";
});

el.composer.addEventListener("submit", (e) => {
  e.preventDefault();
  ask(el.question.value);
});
el.question.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    ask(el.question.value);
  }
});
el.question.addEventListener("input", () => {
  autosize();
  el.send.disabled = el.question.disabled || !el.question.value.trim();
});

el.newChat.addEventListener("click", () => {
  const doc = currentDoc();
  if (!doc || state.streaming) return;
  state.convs[doc.id] = { sessionId: newSessionId(), messages: [] };
  persist(doc.id);
  renderMain();
  el.question.focus();
});

el.menuBtn.addEventListener("click", (e) => {
  e.stopPropagation();
  el.app.classList.toggle("drawer-open");
});
// On narrow screens, tapping outside the open drawer closes it.
document.querySelector(".main").addEventListener("click", () => el.app.classList.remove("drawer-open"));

// Drag and drop a PDF anywhere on the page.
let dragDepth = 0;
const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
window.addEventListener("dragenter", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth++;
  el.dropOverlay.hidden = false;
});
window.addEventListener("dragover", (e) => { if (hasFiles(e)) e.preventDefault(); });
window.addEventListener("dragleave", (e) => {
  if (!hasFiles(e)) return;
  dragDepth = Math.max(0, dragDepth - 1);
  if (dragDepth === 0) el.dropOverlay.hidden = true;
});
window.addEventListener("drop", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth = 0;
  el.dropOverlay.hidden = true;
  uploadFile(e.dataTransfer.files[0]);
});

refreshDocs();
