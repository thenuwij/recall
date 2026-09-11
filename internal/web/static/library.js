import { api, el, showMessage } from "/app.js";

const form = document.getElementById("upload");
const fileInput = document.getElementById("file");
const uploadMessage = document.getElementById("upload-message");
const listMessage = document.getElementById("list-message");
const rows = document.getElementById("documents");

let pollTimer = null;

async function loadDocuments() {
  clearTimeout(pollTimer);

  try {
    const { documents } = await api("/documents");
    showMessage(listMessage, "", false);
    renderDocuments(documents);

    const busy = documents.some((doc) =>
      [doc.status, doc.cards_status].some((status) => status === "queued" || status === "processing"),
    );
    if (busy) {
      pollTimer = setTimeout(loadDocuments, 2000);
    }
  } catch (error) {
    showMessage(listMessage, error.message, true);
  }
}

function renderDocuments(documents) {
  if (documents.length === 0) {
    const cell = el("td", "empty", "No documents yet. Upload a PDF, text, or Markdown file.");
    const row = el("tr");
    row.append(cell);
    rows.replaceChildren(row);
    return;
  }

  rows.replaceChildren(...documents.map(renderDocument));
}

function renderDocument(doc) {
  const row = el("tr");

  const name = el("td");
  name.append(el("div", "", doc.title || "Untitled document"));
  name.append(el("div", "meta", `${doc.source_type.toUpperCase()} · ${new Date(doc.created_at).toLocaleString()}`));

  const status = el("td");
  status.append(el("span", `status ${doc.status}`, doc.status));

  const cards = el("td");
  cards.append(renderCards(doc));

  const actions = el("td");
  const remove = el("button", "danger", "Delete");
  remove.type = "button";
  remove.addEventListener("click", () => deleteDocument(doc, remove));
  actions.append(remove);

  row.append(name, status, cards, actions);
  return row;
}

function renderCards(doc) {
  switch (doc.cards_status) {
    case "ready":
      return el("span", "meta", `${doc.card_count} ${doc.card_count === 1 ? "card" : "cards"}`);
    case "queued":
    case "processing":
      return el("span", "status processing", "writing cards");
    case "failed":
      return el("span", "status failed", "cards failed");
  }
  return el("span", "meta", "");
}

async function deleteDocument(doc, button) {
  if (!confirm(`Delete "${doc.title || "Untitled document"}"?`)) {
    return;
  }

  button.disabled = true;
  try {
    await api(`/documents/${doc.id}`, { method: "DELETE" });
    await loadDocuments();
  } catch (error) {
    showMessage(listMessage, error.message, true);
    button.disabled = false;
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button");
  button.disabled = true;
  showMessage(uploadMessage, "Uploading…", false);

  try {
    const body = new FormData();
    body.append("file", fileInput.files[0]);
    const result = await api("/documents/upload", { method: "POST", body });
    showMessage(uploadMessage, `Uploaded "${result.title || "Untitled document"}". Its questions will appear in Review once processing finishes.`, false);
    form.reset();
    await loadDocuments();
  } catch (error) {
    showMessage(uploadMessage, error.message, true);
  } finally {
    button.disabled = false;
  }
});

loadDocuments();
