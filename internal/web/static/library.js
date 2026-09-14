import { mountAccount, api, el, showMessage } from "/app.js";
const $ = (id) => document.getElementById(id);
let documents = [],
  folders = [],
  selected = "",
  pollTimer,
  editing = "",
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
        const edit = el("button", "icon-button", "⋯");
        edit.setAttribute("aria-label", `Rename ${f.name}`);
        edit.onclick = () => openFolder(f);
        row.append(edit);
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
  let remove = $("delete-folder");
  if (remove) remove.remove();
  if (folder && !account?.is_demo) {
    remove = el("button", "link danger", "Delete folder");
    remove.id = "delete-folder";
    remove.onclick = async () => {
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
    };
    $("documents-title").after(remove);
  }
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
          ? "Move a document here using its folder menu, or upload your notes."
          : "Upload your first set of notes. Recall will turn them into questions you can practise at your own pace.",
      ),
    );
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
  top.append(
    el("span", "document-icon", doc.source_type === "pdf" ? "PDF" : "TXT"),
    el(
      "span",
      `status ${ready ? "ready" : failed ? "failed" : "processing"}`,
      ready
        ? "Ready to review"
        : failed
          ? "Processing failed"
          : "Preparing questions",
    ),
  );
  const title = el("h3"),
    link = el("a", "", doc.title || "Untitled document");
  link.href = `/?document_id=${doc.id}&name=${encodeURIComponent(doc.title || "Document")}`;
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
    review = el("a", "", ready ? "Start review ↗" : "Questions pending");
  review.href = ready
    ? `/?document_id=${doc.id}&name=${encodeURIComponent(doc.title || "Document")}`
    : link.href;
  actions.append(review);
  if (!doc.locked) {
    const remove = el("button", "danger", "Delete");
    remove.setAttribute("aria-label", `Delete ${doc.title || "document"}`);
    remove.onclick = async () => {
      if (
        !confirm(
          `Delete “${doc.title || "Untitled document"}” and its review history?`,
        )
      )
        return;
      remove.disabled = true;
      try {
        await api(`/documents/${doc.id}`, { method: "DELETE" });
        await loadDocuments();
      } catch (e) {
        remove.disabled = false;
        showMessage($("list-message"), e.message, true);
      }
    };
    actions.append(remove);
  }
  card.append(actions);
  if (!doc.locked && !account?.is_demo) {
    const select = el("select", "folder-select");
    select.setAttribute("aria-label", `Folder for ${doc.title || "document"}`);
    const unfiled = el("option", "", "Unfiled");
    unfiled.value = "";
    select.append(unfiled);
    for (const f of folders) {
      const option = el("option", "", f.name);
      option.value = f.id;
      select.append(option);
    }
    select.value = doc.folder_id;
    select.onchange = async () => {
      select.disabled = true;
      try {
        await api(
          `/documents/${doc.id}/folder`,
          json("PATCH", { folder_id: select.value }),
        );
        await loadDocuments();
      } catch (e) {
        select.value = doc.folder_id;
        select.disabled = false;
        showMessage($("list-message"), e.message, true);
      }
    };
    card.append(select);
  }
  return card;
}
$("all-documents").onclick = () => selectFolder("");
$("unfiled-documents").onclick = () => selectFolder("unfiled");
$("new-folder").onclick = () => openFolder();
$("close-folder").onclick = $("cancel-folder").onclick = () =>
  $("folder-dialog").close();
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
