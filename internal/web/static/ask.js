import {
  mountAccount,
  api,
  el,
  showContext,
  showMessage,
  sourceLabel,
} from "/app.js";

const form = document.getElementById("ask");
const question = document.getElementById("question");
const message = document.getElementById("ask-message");
const result = document.getElementById("result");
const context = document.getElementById("context");

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = form.querySelector("button");
  button.disabled = true;
  result.hidden = true;
  context.hidden = true;
  showMessage(message, "Searching your documents…", false);

  try {
    const answer = await api("/answer", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ query: question.value }),
    });
    showMessage(message, "", false);
    renderAnswer(answer);
  } catch (error) {
    showMessage(message, error.message, true);
  } finally {
    button.disabled = false;
  }
});

function renderAnswer(answer) {
  if (answer.refused) {
    const panel = el("div", "panel refusal");
    panel.append(
      el("strong", "", "Your documents don't contain enough to answer this."),
    );
    panel.append(el("p", "message", answer.reason));
    result.replaceChildren(panel);
    result.hidden = false;
    return;
  }

  const citations = new Map(
    answer.citations.map((citation) => [citation.marker, citation]),
  );

  const text = el("div", "answer");
  for (const part of answer.answer.split(/(\[\d+\])/)) {
    const match = part.match(/^\[(\d+)\]$/);
    const citation = match && citations.get(Number(match[1]));
    if (citation) {
      const marker = el("button", "marker", part);
      marker.type = "button";
      marker.title = sourceLabel(citation.title, citation.page);
      marker.addEventListener("click", () =>
        showContext(context, citation.chunk_id),
      );
      text.append(marker);
    } else {
      text.append(document.createTextNode(part));
    }
  }

  const list = el("div", "citations");
  for (const citation of answer.citations) {
    const button = el(
      "button",
      "plain",
      `[${citation.marker}] ${sourceLabel(citation.title, citation.page)}`,
    );
    button.type = "button";
    button.addEventListener("click", () =>
      showContext(context, citation.chunk_id),
    );
    list.append(button);
  }

  const panel = el("div", "panel");
  panel.append(text, list);
  result.replaceChildren(panel);
  result.hidden = false;
}

mountAccount();
