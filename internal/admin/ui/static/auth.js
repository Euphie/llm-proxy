import { createWindowFrame } from "./chrome.js";

export function nextScreen(session) {
  if (!session?.authenticated) {
    return "login";
  }
  if (session.must_change_password) {
    return "password-change";
  }
  return "overview";
}

export function renderLogin(root, handlers) {
  const card = createAuthCard("登录");
  const form = document.createElement("form");
  form.className = "stack";
  const username = createField({
    id: "login-username",
    label: "用户名",
    name: "username",
    autocomplete: "username",
    value: "admin",
  });
  const password = createField({
    id: "login-password",
    label: "密码",
    name: "password",
    type: "password",
    autocomplete: "current-password",
  });
  const alert = createAlert();
  const submit = createSubmitButton("登录");

  form.append(username.field, password.field, alert, submit);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    clearAlert(alert);
    submit.disabled = true;
    try {
      await handlers.login(username.input.value, password.input.value);
    } catch (error) {
      showError(alert, error);
    } finally {
      password.input.value = "";
      submit.disabled = false;
    }
  });

  card.append(form);
  root.replaceChildren(createAuthLayout(card));
}

export function renderPasswordChange(root, handlers) {
  root.replaceChildren(
    createAuthLayout(createPasswordChangeCard("修改初始密码", handlers)),
  );
}

export function renderPasswordChangePanel(root, handlers) {
  root.replaceChildren(createPasswordChangeCard("修改密码", handlers));
}

function createPasswordChangeCard(heading, handlers) {
  const card = createAuthCard(heading);
  const form = document.createElement("form");
  form.className = "stack";
  const currentPassword = createField({
    id: "current-password",
    label: "当前密码",
    name: "current-password",
    type: "password",
    autocomplete: "current-password",
  });
  const newPassword = createField({
    id: "new-password",
    label: "新密码",
    name: "new-password",
    type: "password",
    autocomplete: "new-password",
    description: "至少 10 个字符。",
  });
  const confirmation = createField({
    id: "confirm-password",
    label: "确认新密码",
    name: "confirm-password",
    type: "password",
    autocomplete: "new-password",
  });
  const alert = createAlert();
  const submit = createSubmitButton("保存新密码");
  const passwordInputs = [
    currentPassword.input,
    newPassword.input,
    confirmation.input,
  ];

  form.append(
    currentPassword.field,
    newPassword.field,
    confirmation.field,
    alert,
    submit,
  );
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    clearAlert(alert);
    const currentValue = currentPassword.input.value;
    const newValue = newPassword.input.value;
    const confirmationValue = confirmation.input.value;

    if (currentValue === "") {
      showMessage(alert, "请输入当前密码。");
      clearPasswords(passwordInputs);
      return;
    }
    if (Array.from(newValue).length < 10) {
      showMessage(alert, "新密码至少需要 10 个字符。");
      clearPasswords(passwordInputs);
      return;
    }
    if (newValue !== confirmationValue) {
      showMessage(alert, "两次输入的新密码不一致。");
      clearPasswords(passwordInputs);
      return;
    }

    submit.disabled = true;
    try {
      await handlers.changePassword(currentValue, newValue);
    } catch (error) {
      showError(alert, error);
    } finally {
      clearPasswords(passwordInputs);
      submit.disabled = false;
    }
  });

  card.append(form);
  return card;
}

function createAuthLayout(card) {
  const stage = document.createElement("section");
  stage.className = "desktop-stage auth-stage auth-layout";
  stage.append(
    createWindowFrame({
      titleText: "llm-proxy",
      className: "auth-window",
      children: [card],
    }),
  );
  return stage;
}

function createAuthCard(headingText) {
  const card = document.createElement("div");
  card.className = "card auth-card";
  const heading = document.createElement("h1");
  heading.textContent = headingText;
  card.append(heading);
  return card;
}

function createField({
  id,
  label: labelText,
  name,
  type = "text",
  autocomplete,
  value = "",
  description = "",
}) {
  const field = document.createElement("div");
  field.className = "form-field";
  const label = document.createElement("label");
  label.setAttribute("for", id);
  label.textContent = labelText;
  const input = document.createElement("input");
  input.id = id;
  input.name = name;
  input.type = type;
  input.autocomplete = autocomplete;
  input.required = true;
  input.value = value;
  field.append(label, input);
  if (description !== "") {
    const help = document.createElement("small");
    help.textContent = description;
    field.append(help);
  }
  return { field, input };
}

function createAlert() {
  const alert = document.createElement("div");
  alert.className = "error-banner";
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  return alert;
}

function createSubmitButton(text) {
  const button = document.createElement("button");
  button.className = "button";
  button.type = "submit";
  button.textContent = text;
  return button;
}

function clearAlert(alert) {
  alert.hidden = true;
  alert.textContent = "";
}

function showError(alert, error) {
  showMessage(alert, error?.message || "请求失败，请重试。");
}

function showMessage(alert, message) {
  alert.textContent = message;
  alert.hidden = false;
}

function clearPasswords(inputs) {
  for (const input of inputs) {
    input.value = "";
  }
}
