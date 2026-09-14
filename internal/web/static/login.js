import { api, showMessage } from "/app.js";

const form = document.getElementById("login-form");
const title = document.getElementById("form-title");
const submit = document.getElementById("submit");
const message = document.getElementById("login-message");
const switchButton = document.getElementById("switch");
const switchPrompt = document.getElementById("switch-prompt");
const password = document.getElementById("password");

let registering = false;

function applyMode() {
  title.textContent = registering ? "A fresh start." : "Welcome back.";
  submit.textContent = registering ? "Create account →" : "Sign in →";
  switchPrompt.textContent = registering
    ? "Already have an account?"
    : "New here?";
  switchButton.textContent = registering ? "Sign in" : "Create an account";
  password.autocomplete = registering ? "new-password" : "current-password";
  showMessage(message, "", false);
}

switchButton.addEventListener("click", () => {
  registering = !registering;
  applyMode();
});

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  submit.disabled = true;
  showMessage(message, "", false);

  const body = JSON.stringify({
    email: document.getElementById("email").value,
    password: password.value,
  });

  try {
    await api(registering ? "/auth/register" : "/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      allowUnauthorized: true,
    });
    window.location.href = "/";
  } catch (error) {
    showMessage(message, error.message, true);
    submit.disabled = false;
  }
});

applyMode();

document
  .getElementById("demo-login")
  .addEventListener("click", async (event) => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      await api("/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email: "demo@recall.app",
          password: "recalldemo123",
        }),
        allowUnauthorized: true,
      });
      location.href = "/library.html";
    } catch (error) {
      showMessage(message, error.message, true);
      button.disabled = false;
    }
  });
