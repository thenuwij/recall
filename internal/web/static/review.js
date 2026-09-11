import { api, el, renderPassage, showMessage, sourceLabel } from "/app.js";

const dueCount = document.getElementById("due-count");
const message = document.getElementById("review-message");
const empty = document.getElementById("empty");
const cardPanel = document.getElementById("card");
const cardSource = document.getElementById("card-source");
const cardQuestion = document.getElementById("card-question");
const form = document.getElementById("answer-form");
const answer = document.getElementById("answer");
const result = document.getElementById("result");
const resultGrade = document.getElementById("result-grade");
const resultRationale = document.getElementById("result-rationale");
const resultExpected = document.getElementById("result-expected");
const resultNext = document.getElementById("result-next");
const resultSource = document.getElementById("result-source");
const nextButton = document.getElementById("next-card");

let queue = [];
let position = 0;

async function loadQueue() {
  cardPanel.hidden = true;
  result.hidden = true;
  empty.hidden = true;
  showMessage(message, "", false);

  try {
    const due = await api("/reviews/due?limit=100");
    queue = due.cards;
    position = 0;
    if (queue.length === 0) {
      showEmpty(due.next_due_at);
      return;
    }
    showCard();
  } catch (error) {
    dueCount.textContent = "";
    showMessage(message, error.message, true);
  }
}

function showEmpty(nextDueAt) {
  dueCount.textContent = "";
  if (nextDueAt) {
    empty.replaceChildren(
      el("strong", "", "All caught up."),
      el("p", "message", `Your next card is due ${describeDue(nextDueAt)}.`),
    );
  } else {
    const text = el("p", "message", "Nothing is scheduled. Upload lecture notes or slides in the ");
    const link = el("a", "", "Library");
    link.href = "/library.html";
    text.append(link, document.createTextNode(" and Recall will write questions from them."));
    empty.replaceChildren(el("strong", "", "No cards due."), text);
  }
  empty.hidden = false;
}

function showCard() {
  const card = queue[position];
  const remaining = queue.length - position;
  dueCount.textContent = `${remaining} ${remaining === 1 ? "card" : "cards"} due`;

  cardSource.textContent = card.is_new ? `New · ${sourceLabel(card.title, card.page)}` : sourceLabel(card.title, card.page);
  cardQuestion.textContent = card.question;
  answer.value = "";
  result.hidden = true;
  cardPanel.hidden = false;
  answer.disabled = false;
  form.querySelector("button").disabled = false;
  answer.focus();
}

async function submitAnswer() {
  const card = queue[position];
  const button = form.querySelector("button");
  button.disabled = true;
  answer.disabled = true;
  showMessage(message, "Checking your answer against the source…", false);

  try {
    const review = await api(`/reviews/${card.card_id}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ answer: answer.value }),
    });
    showMessage(message, "", false);
    showResult(review);
  } catch (error) {
    showMessage(message, error.message, true);
    button.disabled = false;
    answer.disabled = false;
  }
}

function showResult(review) {
  resultGrade.textContent = `${review.label} · ${review.score}/5`;
  resultGrade.className = `grade ${gradeClass(review.score)}`;
  resultRationale.textContent = review.rationale;
  resultExpected.textContent = review.expected_answer;
  resultNext.textContent = `This card returns ${describeDue(review.next_due_at)}.`;

  if (review.source) {
    renderPassage(resultSource, review.source);
    resultSource.hidden = false;
  } else {
    resultSource.hidden = true;
  }

  result.hidden = false;
  nextButton.focus();
}

function gradeClass(score) {
  if (score <= 2) {
    return "low";
  }
  if (score === 3) {
    return "mid";
  }
  return "high";
}

function describeDue(value) {
  const due = new Date(value);
  const hours = Math.round((due - Date.now()) / 3600000);
  const when = due.toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "numeric", minute: "2-digit" });
  if (hours < 1) {
    return `within the hour (${when})`;
  }
  if (hours < 24) {
    return `in ${hours} ${hours === 1 ? "hour" : "hours"} (${when})`;
  }
  const days = Math.round(hours / 24);
  return `in ${days} ${days === 1 ? "day" : "days"} (${when})`;
}

form.addEventListener("submit", (event) => {
  event.preventDefault();
  submitAnswer();
});

answer.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
    event.preventDefault();
    form.requestSubmit();
  }
});

nextButton.addEventListener("click", () => {
  position++;
  if (position < queue.length) {
    showCard();
    return;
  }
  loadQueue();
});

loadQueue();
