import { api, mountAccount, showMessage } from "/app.js";
const $ = (id) => document.getElementById(id);
const params = new URLSearchParams(location.search),
  id = params.get("id");
let pageNumber = Math.max(1, parseInt(params.get("page") || "1", 10) || 1),
  pdf,
  renderTask,
  renderVersion = 0,
  resizeTimer;
async function renderPage() {
  if (!pdf) return;
  const version = ++renderVersion;
  if (renderTask) {
    renderTask.cancel();
    try {
      await renderTask.promise;
    } catch {}
    renderTask = null;
  }
  try {
    const page = await pdf.getPage(pageNumber);
    if (version !== renderVersion) return;
    const base = page.getViewport({ scale: 1 });
    const width = Math.min($("pdf-stage").clientWidth - 32, 1000);
    const viewport = page.getViewport({
      scale: (width / base.width) * Number($("pdf-zoom").value),
    });
    const pixelRatio = Math.min(window.devicePixelRatio || 1, 2),
      canvas = $("pdf-canvas");
    canvas.width = Math.floor(viewport.width * pixelRatio);
    canvas.height = Math.floor(viewport.height * pixelRatio);
    canvas.style.width = `${viewport.width}px`;
    canvas.style.height = `${viewport.height}px`;
    canvas.setAttribute(
      "aria-label",
      `Original PDF, page ${pageNumber} of ${pdf.numPages}`,
    );
    $("page-number").value = pageNumber;
    $("page-number").max = pdf.numPages;
    $("page-total").textContent = `of ${pdf.numPages}`;
    $("previous-page").disabled = pageNumber <= 1;
    $("next-page").disabled = pageNumber >= pdf.numPages;
    renderTask = page.render({
      canvas,
      viewport,
      transform: pixelRatio === 1 ? null : [pixelRatio, 0, 0, pixelRatio, 0, 0],
    });
    await renderTask.promise;
    if (version !== renderVersion) return;
    const text = await page.getTextContent();
    if (version !== renderVersion) return;
    $("pdf-page-text").textContent = text.items
      .map((item) => item.str + (item.hasEOL ? "\n" : " "))
      .join("");
    showMessage($("viewer-message"), "", false);
  } catch (error) {
    if (
      version !== renderVersion ||
      error.name === "RenderingCancelledException"
    )
      return;
    showMessage(
      $("viewer-message"),
      "This page couldn’t be rendered. You can still open the original PDF in a new tab.",
      true,
    );
  }
}
function goToPage(number) {
  pageNumber = Math.min(pdf.numPages, Math.max(1, number || 1));
  renderPage();
}
$("previous-page").onclick = () => goToPage(pageNumber - 1);
$("next-page").onclick = () => goToPage(pageNumber + 1);
$("page-number").onchange = (event) =>
  goToPage(parseInt(event.target.value, 10));
$("pdf-zoom").onchange = renderPage;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(renderPage, 150);
});
window.addEventListener("pagehide", () => {
  clearTimeout(resizeTimer);
  renderVersion++;
  renderTask?.cancel();
  pdf?.destroy();
});
try {
  await mountAccount();
  if (!id) throw new Error("Choose a document from your library.");
  const doc = await api(`/documents/${encodeURIComponent(id)}`);
  $("viewer-title").textContent = doc.title || "Untitled document";
  $("document-review").href =
    `/?document_id=${encodeURIComponent(id)}&name=${encodeURIComponent(doc.title || "Document")}`;
  $("viewer-actions").hidden = false;
  if (doc.has_pdf) {
    const url = `/documents/${encodeURIComponent(id)}/pdf`;
    $("open-pdf").href = `${url}#page=${pageNumber}`;
    showMessage($("viewer-message"), "Opening your original PDF…", false);
    const pdfjs = await import("/vendor/pdfjs/build/pdf.min.mjs");
    pdfjs.GlobalWorkerOptions.workerSrc =
      "/vendor/pdfjs/build/pdf.worker.min.mjs";
    pdf = await pdfjs.getDocument({
      url,
      cMapUrl: "/vendor/pdfjs/cmaps/",
      cMapPacked: true,
      standardFontDataUrl: "/vendor/pdfjs/standard_fonts/",
      wasmUrl: "/vendor/pdfjs/wasm/",
      iccUrl: "/vendor/pdfjs/iccs/",
      isEvalSupported: false,
    }).promise;
    pageNumber = Math.min(pageNumber, pdf.numPages);
    $("pdf-reader").hidden = false;
    await renderPage();
  } else {
    $("open-pdf").hidden = true;
    $("text-document").textContent = doc.content;
    $("text-document").hidden = false;
    if (doc.source_type === "pdf")
      showMessage(
        $("viewer-message"),
        "This older upload has extracted text only. Re-upload the PDF from your library to view the original file. Your existing review progress is unchanged.",
        false,
      );
  }
} catch (error) {
  showMessage($("viewer-message"), error.message, true);
}
