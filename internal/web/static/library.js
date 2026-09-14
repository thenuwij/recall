import { mountAccount, api, el, showMessage } from "/app.js";
const $ = (id) => document.getElementById(id);
let documents = [],
  folders = [],
  selected = "",
  pollTimer,
  editing = "",
  movingDocument,
  addingFolder,
  pickerSelection = new Set(),
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
    if (
      selected &&
      selected !== "unfiled" &&
      !folders.some((f) => f.id === selected)
    )
      selected = "";
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
function selectFolder(id) {
  selected = id;
  render();
}
function openFolder(f) {
  editing = f?.id || "";
  $("folder-name").value = f?.name || "";
  $("folder-dialog-title").textContent = f ? "Rename folder" : "New folder";
  showMessage($("folder-message"), "", false);
  $("folder-dialog").showModal();
  $("folder-name").focus();
}
function folderName(id) {
  return folders.find((folder) => folder.id === id)?.name || "Unfiled";
}
async function deleteFolder(folder) {
  if (
    !confirm(
      `Delete “${folder.name}”? Its documents will remain in your library, unfiled.`,
    )
  )
    return;
  try {
    await api(`/folders/${folder.id}`, { method: "DELETE" });
    selected = "";
    await loadDocuments();
  } catch (e) {
    showMessage($("list-message"), e.message, true);
  }
}
async function deleteDocument(doc, button) {
  if (
    !confirm(
      `Delete “${doc.title || "Untitled document"}” and its review history?`,
    )
  )
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
function menu(items, className = "document-menu") {
  const details = el("details", className),
    summary = el("summary", "", "⋯"),
    panel = el("div", "menu-panel");
  summary.setAttribute("aria-label", "More options");
  panel.addEventListener("click", () => {
    details.open = false;
  });
  panel.append(...items);
  details.append(summary, panel);
  return details;
}
function openMoveDialog(doc) {
  movingDocument = doc;
  $("move-dialog-title").textContent =
    `Move “${doc.title || "Untitled document"}”`;
  const unfiled = el("option", "", "Unfiled");
  unfiled.value = "";
  $("move-folder").replaceChildren(
    unfiled,
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
async function removeFromFolder(doc, button) {
  button.disabled = true;
  try {
    await api(`/documents/${doc.id}/folder`, json("PATCH", { folder_id: "" }));
    await loadDocuments();
  } catch (e) {
    button.disabled = false;
    showMessage($("list-message"), e.message, true);
  }
}
function pickerCandidates() {
  if (!addingFolder) return [];
  const query = $("document-search").value.trim().toLocaleLowerCase();
  return documents.filter(
    (doc) =>
      !doc.locked &&
      doc.folder_id !== addingFolder.id &&
      (!query ||
        (doc.title || "Untitled document").toLocaleLowerCase().includes(query)),
  );
}
function updatePickerSelection() {
  const count = pickerSelection.size;
  $("selected-document-count").textContent = `${count} selected`;
  $("confirm-add-documents").disabled = count === 0;
}
function renderDocumentPicker() {
  const candidates = pickerCandidates();
  if (!candidates.length) {
    $("document-picker-list").replaceChildren(
      el(
        "div",
        "document-picker-empty",
        $("document-search").value
          ? "No documents match your search."
          : "Every available document is already in this folder.",
      ),
    );
    updatePickerSelection();
    return;
  }
  $("document-picker-list").replaceChildren(
    ...candidates.map((doc) => {
      const label = el("label", "document-picker-item"),
        checkbox = el("input"),
        copy = el("span");
      checkbox.type = "checkbox";
      checkbox.value = doc.id;
      checkbox.checked = pickerSelection.has(doc.id);
      checkbox.onchange = () => {
        if (checkbox.checked) pickerSelection.add(doc.id);
        else pickerSelection.delete(doc.id);
        updatePickerSelection();
      };
      copy.append(
        el("strong", "", doc.title || "Untitled document"),
        el("span", "", `Currently in: ${folderName(doc.folder_id)}`),
      );
      label.append(checkbox, copy);
      return label;
    }),
  );
  updatePickerSelection();
}
function openAddDocuments(folder) {
  addingFolder = folder;
  pickerSelection = new Set();
  $("add-documents-title").textContent = `Add to “${folder.name}”`;
  $("document-search").value = "";
  showMessage($("add-documents-message"), "", false);
  renderDocumentPicker();
  $("add-documents-dialog").showModal();
  $("document-search").focus();
}
function render() {
  $("all-count").textContent = documents.length;
  $("all-documents").classList.toggle("selected", selected === "");
  $("unfiled-documents").classList.toggle("selected", selected === "unfiled");
  $("folders").replaceChildren(
    ...folders.map((f) => {
      const row = el("div", "folder-row"),
        button = el(
          "button",
          `side-item ${selected === f.id ? "selected" : ""}`,
          f.name,
        );
      button.onclick = () => selectFolder(f.id);
      row.append(button);
      if (!account?.is_demo) {
        const rename = el("button", "", "Rename"),
          remove = el("button", "danger", "Delete folder");
        rename.type = remove.type = "button";
        rename.onclick = () => openFolder(f);
        remove.onclick = () => deleteFolder(f);
        const options = menu([rename, remove], "folder-menu");
        options
          .querySelector("summary")
          .setAttribute("aria-label", `Options for ${f.name}`);
        row.append(options);
      }
      return row;
    }),
  );
  const folder = folders.find((f) => f.id === selected);
  $("library-title").textContent =
    folder?.name || (selected === "unfiled" ? "Unfiled notes" : "Your library");
  $("documents-title").textContent = folder
    ? "Documents in this folder"
    : selected === "unfiled"
      ? "Unfiled documents"
      : "All documents";
  $("review-folder").href = folder
    ? `/?folder_id=${folder.id}&name=${encodeURIComponent(folder.name)}`
    : "/";
  $("review-folder").hidden = selected === "unfiled";
  $("review-folder").firstChild.textContent = folder
    ? "Review this folder "
    : "Review all due ";
  $("add-documents").hidden = !folder || account?.is_demo;
  $("add-documents").onclick = folder ? () => openAddDocuments(folder) : null;
  const shown = documents.filter(
    (d) =>
      !selected ||
      (selected === "unfiled" ? !d.folder_id : d.folder_id === selected),
  );
  $("document-count").textContent =
    `${shown.length} ${shown.length === 1 ? "document" : "documents"}`;
  if (!shown.length) {
    const box = el("div", "empty-state");
    box.append(
      el(
        "strong",
        "",
        selected ? "Room for new ideas." : "Your next idea starts here.",
      ),
      el(
        "p",
        "",
        selected
          ? "Upload new notes above, or add documents already in your library."
          : "Upload your first set of notes. Recall will turn them into questions you can practise at your own pace.",
      ),
    );
    if (folder && !account?.is_demo) {
      const add = el("button", "secondary", "+ Add existing documents");
      add.type = "button";
      add.onclick = () => openAddDocuments(folder);
      box.append(add);
    }
    $("documents").replaceChildren(box);
    return;
  }
  $("documents").replaceChildren(...shown.map(renderDocument));
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
          : "Preparing questions",
    ),
    cardMenuItems = [],
    open = el("a", "", "Open document");
  open.href = `/viewer.html?id=${doc.id}`;
  cardMenuItems.push(open);
  if (!doc.locked && !account?.is_demo) {
    const move = el(
      "button",
      "",
      doc.folder_id ? "Move to another folder…" : "Move to folder…",
    );
    move.type = "button";
    move.onclick = () => openMoveDialog(doc);
    cardMenuItems.push(move);
    if (doc.folder_id) {
      const unfile = el("button", "", "Remove from folder");
      unfile.type = "button";
      unfile.onclick = () => removeFromFolder(doc, unfile);
      cardMenuItems.push(unfile);
    }
  }
  if (!doc.locked) {
    const remove = el("button", "danger", "Delete document");
    remove.type = "button";
    remove.onclick = () => deleteDocument(doc, remove);
    cardMenuItems.push(remove);
  }
  const options = menu(cardMenuItems),
    topActions = el("div", "card-top-actions");
  options
    .querySelector("summary")
    .setAttribute("aria-label", `Options for ${doc.title || "document"}`);
  topActions.append(status, options);
  top.append(
    el("span", "document-icon", doc.source_type === "pdf" ? "PDF" : "TXT"),
    topActions,
  );
  const title = el("h3"),
    link = el("a", "", doc.title || "Untitled document");
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
    el("div", "folder-label", `Folder: ${folderName(doc.folder_id)}`),
  );
  const actions = el("div", "document-actions"),
    review = el("a", `button document-review ${ready ? "" : "secondary"}`),
    reviewLabel = el("span", "", ready ? "Review questions" : "Open document");
  review.append(reviewLabel, el("span", "", "→"));
  review.href = ready
    ? `/?document_id=${doc.id}&name=${encodeURIComponent(doc.title || "Document")}`
    : link.href;
  actions.append(review);
  card.append(actions);
  return card;
}
$("all-documents").onclick = () => selectFolder("");
$("unfiled-documents").onclick = () => selectFolder("unfiled");
$("new-folder").onclick = () => openFolder();
$("close-folder").onclick = $("cancel-folder").onclick = () =>
  $("folder-dialog").close();
$("close-move").onclick = $("cancel-move").onclick = () =>
  $("move-dialog").close();
$("close-add-documents").onclick = $("cancel-add-documents").onclick = () =>
  $("add-documents-dialog").close();
$("folder-form").onsubmit = async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    const f = await api(
      editing ? `/folders/${editing}` : "/folders",
      json(editing ? "PATCH" : "POST", { name: $("folder-name").value }),
    );
    selected = f.id;
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
$("document-search").oninput = renderDocumentPicker;
$("add-documents-form").onsubmit = async (event) => {
  event.preventDefault();
  const button = event.submitter,
    ids = [...pickerSelection];
  if (!ids.length) return;
  button.disabled = true;
  showMessage(
    $("add-documents-message"),
    `Moving ${ids.length} ${ids.length === 1 ? "document" : "documents"}…`,
    false,
  );
  const results = await Promise.allSettled(
    ids.map((id) =>
      api(
        `/documents/${id}/folder`,
        json("PATCH", { folder_id: addingFolder.id }),
      ),
    ),
  );
  const failures = results.filter((result) => result.status === "rejected");
  await loadDocuments();
  if (!failures.length) {
    $("add-documents-dialog").close();
    return;
  }
  pickerSelection = new Set(
    failures.map((failure) => ids[results.indexOf(failure)]),
  );
  showMessage(
    $("add-documents-message"),
    `${ids.length - failures.length} moved. ${failures.length} could not be moved.`,
    true,
  );
  renderDocumentPicker();
};
$("file").onchange = () => {
  $("file-name").textContent = $("file").files[0]?.name || "";
  $("upload-button").hidden = !$("file").files.length;
};
$("upload").onsubmit = async (event) => {
  event.preventDefault();
  const file = $("file").files[0];
  if (!file) return;
  $("upload-button").disabled = true;
  showMessage($("upload-message"), "Uploading your notes…", false);
  try {
    const body = new FormData();
    body.append("file", file);
    const doc = await api("/documents/upload", { method: "POST", body });
    $("upload").reset();
    $("file-name").textContent = "";
    $("upload-button").hidden = true;
    if (selected && selected !== "unfiled" && !account?.is_demo) {
      try {
        await api(
          `/documents/${doc.id}/folder`,
          json("PATCH", { folder_id: selected }),
        );
      } catch (e) {
        showMessage(
          $("upload-message"),
          `Uploaded, but couldn't move to the folder: ${e.message}`,
          true,
        );
        await loadDocuments();
        return;
      }
    }
    showMessage(
      $("upload-message"),
      `“${doc.title || "Your document"}” is uploaded. We’re preparing your questions.`,
      false,
    );
    $("upload").reset();
    $("file-name").textContent = "";
    $("upload-button").hidden = true;
    await loadDocuments();
  } catch (e) {
    showMessage($("upload-message"), e.message, true);
  } finally {
    $("upload-button").disabled = false;
  }
};
try {
  account = await mountAccount();
  $("new-folder").hidden = account?.is_demo === true;
  await loadDocuments();
} catch (e) {
  showMessage($("list-message"), e.message, true);
}
window.addEventListener("pagehide", () => clearTimeout(pollTimer));
