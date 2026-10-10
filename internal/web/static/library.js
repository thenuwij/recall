import { mountAccount, api, el, showMessage } from "/app.js";
const $ = (id) => document.getElementById(id);
const selected = new URLSearchParams(location.search).get("folder") || "";
let documents = [],
  folders = [],
  pollTimer,
  editing = "",
  movingDocument,
  account;
const json = (method, value) => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(value),
});
async function loadDocuments() {
  clearTimeout(pollTimer);
  try {
    const [d, f] = await Promise.all([api("/documents"), api("/folders")]);
    documents = d.documents;
    folders = f.folders;
    if (selected && !folders.some((f) => f.id === selected)) {
      location.href = "/library.html";
      return;
    }
    render();
    showMessage($("list-message"), "", false);
    if (
      documents.some((d) =>
        [d.status, d.cards_status].some(
          (s) => s === "queued" || s === "processing",
        ),
      )
    )
      pollTimer = setTimeout(loadDocuments, 2500);
  } catch (e) {
    showMessage($("list-message"), e.message, true);
  }
}
function openFolder(f) {
  editing = f?.id || "";
  $("folder-name").value = f?.name || "";
  $("folder-dialog-title").textContent = f ? "Rename folder" : "New folder";
  showMessage($("folder-message"), "", false);
  $("folder-dialog").showModal();
  $("folder-name").focus();
}
async function deleteFolder(folder) {
  if (
    !confirm(
      `Delete “${folder.name}”? Its notes stay in Notes, outside any folder.`,
    )
  )
    return;
  try {
    await api(`/folders/${folder.id}`, { method: "DELETE" });
    location.href = "/library.html";
  } catch (e) {
    showMessage($("list-message"), e.message, true);
  }
}
async function deleteDocument(doc, button) {
  if (!confirm(`Delete “${doc.title || "Untitled note"}” and its questions?`))
    return;
  button.disabled = true;
  try {
    await api(`/documents/${doc.id}`, { method: "DELETE" });
    await loadDocuments();
  } catch (e) {
    button.disabled = false;
    showMessage($("list-message"), e.message, true);
  }
}
function menu(items, label) {
  const details = el("details", "document-menu"),
    summary = el("summary", "", "⋯"),
    panel = el("div", "menu-panel");
  summary.setAttribute("aria-label", label);
  panel.addEventListener("click", () => {
    details.open = false;
  });
  panel.append(...items);
  details.append(summary, panel);
  return details;
}
function openMoveDialog(doc) {
  movingDocument = doc;
  $("move-dialog-title").textContent = `Move “${doc.title || "Untitled note"}”`;
  const none = el("option", "", "No folder");
  none.value = "";
  $("move-folder").replaceChildren(
    none,
    ...folders.map((folder) => {
      const option = el("option", "", folder.name);
      option.value = folder.id;
      return option;
    }),
  );
  $("move-folder").value = doc.folder_id || "";
  showMessage($("move-message"), "", false);
  $("move-dialog").showModal();
  $("move-folder").focus();
}
function render() {
  const folder = folders.find((f) => f.id === selected);
  $("library-title").textContent = folder ? folder.name : "Notes";
  $("back-to-notes").hidden = !folder;
  $("new-folder").hidden = Boolean(folder) || account?.is_demo === true;
  $("upload").hidden = Boolean(folder);
  $("library-subtitle").hidden = Boolean(folder);
  $("review-folder").hidden = !folder;
  if (folder)
    $("review-folder").href =
      `/?folder_id=${folder.id}&name=${encodeURIComponent(folder.name)}`;
  const shownFolders = folder ? [] : folders;
  $("folders-title").hidden = !shownFolders.length;
  $("folders").replaceChildren(...shownFolders.map(renderFolder));
  const shown = documents.filter((d) => (d.folder_id || "") === selected);
  if (!shown.length && !folder && folders.length) {
    $("documents").replaceChildren();
    return;
  }
  if (!shown.length) {
    const box = el("div", "empty-state");
    box.append(
      el("strong", "", folder ? "This folder is empty." : "No notes yet."),
      el(
        "p",
        "",
        folder
          ? "Choose Move to folder… on a note in Notes to add it here."
          : "Upload notes to get started. Recall writes questions from them, and Review asks them back.",
      ),
    );
    $("documents").replaceChildren(box);
    return;
  }
  $("documents").replaceChildren(...shown.map(renderDocument));
}
function renderFolder(folder) {
  const card = el("article", "document-card folder-card"),
    count = documents.filter((d) => d.folder_id === folder.id).length,
    link = el("a", "folder-link");
  link.href = `/library.html?folder=${folder.id}`;
  link.append(
    el("h3", "", folder.name),
    el("div", "card-meta", `${count} ${count === 1 ? "note" : "notes"}`),
  );
  card.append(link);
  if (!account?.is_demo) {
    const rename = el("button", "", "Rename"),
      remove = el("button", "danger", "Delete folder");
    rename.type = remove.type = "button";
    rename.onclick = () => openFolder(folder);
    remove.onclick = () => deleteFolder(folder);
    card.append(menu([rename, remove], `Options for ${folder.name}`));
  }
  return card;
}
function renderDocument(doc) {
  const card = el("article", "document-card"),
    top = el("div", "document-card-top");
  const ready = doc.cards_status === "ready",
    failed = doc.status === "failed" || doc.cards_status === "failed";
  const status = el(
      "span",
      `status ${ready ? "ready" : failed ? "failed" : "processing"}`,
      ready
        ? "Ready to review"
        : failed
          ? "Processing failed"
          : "Writing questions",
    ),
    cardMenuItems = [],
    open = el("a", "", "Open note");
  open.href = `/viewer.html?id=${doc.id}`;
  cardMenuItems.push(open);
  if (!doc.locked && !account?.is_demo && folders.length) {
    const move = el("button", "", "Move to folder…");
    move.type = "button";
    move.onclick = () => openMoveDialog(doc);
    cardMenuItems.push(move);
  }
  if (!doc.locked) {
    const remove = el("button", "danger", "Delete note");
    remove.type = "button";
    remove.onclick = () => deleteDocument(doc, remove);
    cardMenuItems.push(remove);
  }
  const topActions = el("div", "card-top-actions");
  topActions.append(
    status,
    menu(cardMenuItems, `Options for ${doc.title || "note"}`),
  );
  top.append(
    el("span", "document-icon", doc.source_type === "pdf" ? "PDF" : "TXT"),
    topActions,
  );
  const title = el("h3"),
    link = el("a", "", doc.title || "Untitled note");
  link.href = open.href;
  title.append(link);
  const date = new Date(doc.created_at).toLocaleDateString([], {
    month: "short",
    day: "numeric",
  });
  card.append(
    top,
    title,
    el(
      "div",
      "card-meta",
      `${date} · ${doc.card_count} ${doc.card_count === 1 ? "question" : "questions"}`,
    ),
  );
  const actions = el("div", "document-actions"),
    review = el("a", `button document-review ${ready ? "" : "secondary"}`),
    reviewLabel = el("span", "", ready ? "Review questions" : "Open note");
  review.append(reviewLabel, el("span", "", "→"));
  review.href = ready
    ? `/?document_id=${doc.id}&name=${encodeURIComponent(doc.title || "Note")}`
    : link.href;
  actions.append(review);
  card.append(actions);
  return card;
}
$("new-folder").onclick = () => openFolder();
$("close-folder").onclick = $("cancel-folder").onclick = () =>
  $("folder-dialog").close();
$("close-move").onclick = $("cancel-move").onclick = () =>
  $("move-dialog").close();
$("folder-form").onsubmit = async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    await api(
      editing ? `/folders/${editing}` : "/folders",
      json(editing ? "PATCH" : "POST", { name: $("folder-name").value }),
    );
    $("folder-dialog").close();
    await loadDocuments();
  } catch (e) {
    showMessage($("folder-message"), e.message, true);
  } finally {
    button.disabled = false;
  }
};
$("move-form").onsubmit = async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    await api(
      `/documents/${movingDocument.id}/folder`,
      json("PATCH", { folder_id: $("move-folder").value }),
    );
    $("move-dialog").close();
    await loadDocuments();
  } catch (e) {
    showMessage($("move-message"), e.message, true);
  } finally {
    button.disabled = false;
  }
};
$("file").onchange = () => {
  if ($("file").files.length) $("upload").requestSubmit();
};
$("upload").onsubmit = async (event) => {
  event.preventDefault();
  const file = $("file").files[0];
  if (!file) return;
  $("file").disabled = true;
  showMessage($("upload-message"), `Uploading “${file.name}”…`, false);
  try {
    const body = new FormData();
    body.append("file", file);
    const doc = await api("/documents/upload", { method: "POST", body });
    showMessage(
      $("upload-message"),
      `“${doc.title || "Your note"}” is uploaded. Its questions will be ready in about a minute.`,
      false,
    );
    await loadDocuments();
  } catch (e) {
    showMessage($("upload-message"), e.message, true);
  } finally {
    $("upload").reset();
    $("file").disabled = false;
  }
};
try {
  account = await mountAccount();
  await loadDocuments();
} catch (e) {
  showMessage($("list-message"), e.message, true);
}
window.addEventListener("pagehide", () => clearTimeout(pollTimer));
