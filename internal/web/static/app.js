export async function api(path, options) {
  const response = await fetch(path, options);
  let body = null;
  if (response.status !== 204) {
    body = await response.json().catch(() => null);
  }
  if (!response.ok) {
    throw new Error(body && body.error ? body.error : `Request failed (${response.status})`);
  }
  return body;
}

export function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text !== undefined) {
    node.textContent = text;
  }
  return node;
}

export function sourceLabel(title, page) {
  const name = title || "Untitled document";
  return page ? `${name} · page ${page}` : name;
}

export function showMessage(node, text, isError) {
  node.textContent = text;
  node.className = isError ? "message error" : "message";
  node.hidden = !text;
}

export async function showContext(container, chunkID) {
  container.replaceChildren(el("p", "empty", "Loading passage…"));
  container.hidden = false;

  try {
    const context = await api(`/chunks/${chunkID}/context`);
    const mark = renderPassage(container, context);
    mark.scrollIntoView({ block: "center" });
  } catch (error) {
    container.replaceChildren(el("p", "message error", error.message));
  }
}

export function renderPassage(container, context) {
  const text = el("div", "text");
  const mark = el("mark", "", context.passage);
  text.append(document.createTextNode(context.before), mark, document.createTextNode(context.after));
  container.replaceChildren(el("div", "source", sourceLabel(context.title, context.page)), text);
  return mark;
}
