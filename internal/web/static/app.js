export async function api(path, options) {
  const settings = { ...options };
  const allowUnauthorized = settings.allowUnauthorized === true;
  delete settings.allowUnauthorized;

  const response = await fetch(path, settings);
  if (response.status === 401 && !allowUnauthorized) {
    window.location.href = "/login.html";
    throw new Error("Sign in to continue");
  }

  let body = null;
  if (response.status !== 204) {
    body = await response.json().catch(() => null);
  }
  if (!response.ok) {
    throw new Error(
      body && body.error ? body.error : `Request failed (${response.status})`,
    );
  }
  return body;
}

export async function mountAccount() {
  const container = document.getElementById("account");
  if (!container) {
    return null;
  }

  const account = await api("/auth/me");
  const signOut = el("button", "link", "Sign out");
  signOut.type = "button";
  signOut.addEventListener("click", async () => {
    await api("/auth/logout", { method: "POST", allowUnauthorized: true });
    window.location.href = "/login.html";
  });

  container.replaceChildren(el("span", "meta", account.email), signOut);

  return account;
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
  text.append(
    document.createTextNode(context.before),
    mark,
    document.createTextNode(context.after),
  );
  container.replaceChildren(
    el("div", "source", sourceLabel(context.title, context.page)),
    text,
  );
  if (context.page && context.document_id) {
    const link = el(
      "a",
      "source-link",
      `Open original PDF · page ${context.page} ↗`,
    );
    link.href = `/viewer.html?id=${encodeURIComponent(context.document_id)}&page=${context.page}`;
    link.target = "_blank";
    link.rel = "noopener";
    container.append(link);
  }
  return mark;
}
