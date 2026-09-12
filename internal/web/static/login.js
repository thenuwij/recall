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
  title.textContent = registering ? "Create an account" : "Sign in";
  submit.textContent = registering ? "Create account" : "Sign in";
  switchPrompt.textContent = registering ? "Already have an account?" : "New here?";
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
