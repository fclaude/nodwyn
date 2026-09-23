// Cross-session state is independent of transcript selection. It carries no
// prompt contents; desktop notifications are explicit, generic, and tab-local.
export class Attention {
  constructor(button, summary, changed, open) {
    this.button = button;
    this.summary = summary;
    this.changed = changed;
    this.open = open;
    this.states = new Map();
    this.notifications = new Map();
    this.enabled = false;
    this.connected = false;
    button.addEventListener("click", () => this.toggle());
    this.render();
  }
  reset(connected = false) {
    this.connected = connected;
    this.states.clear();
    for (const notification of this.notifications.values())
      notification.close();
    this.notifications.clear();
    this.render();
  }
  update(state, notify = true) {
    const previous = this.states.get(state.id);
    if (previous && previous.revision >= state.revision) return;
    this.states.set(state.id, state);
    if (!state.open || !state.needs_answer) {
      this.notifications.get(state.id)?.close();
      this.notifications.delete(state.id);
    } else if (
      notify &&
      !previous?.needs_answer &&
      this.enabled &&
      document.hidden &&
      Notification.permission === "granted"
    ) {
      try {
        const notification = new Notification("nibb needs your answer", {
          body: "Open the session desk to review the pending prompt.",
          tag: `nibb-${state.id}`,
        });
        notification.onclick = () => {
          window.focus();
          this.open(state.id);
          notification.close();
        };
        this.notifications.set(state.id, notification);
      } catch {
        this.enabled = false;
      }
    }
    this.changed(state);
    this.render();
  }
  async toggle() {
    if (this.enabled) this.enabled = false;
    else {
      try {
        this.enabled = (await Notification.requestPermission()) === "granted";
      } catch {
        this.enabled = false;
      }
    }
    if (!this.enabled) {
      for (const notification of this.notifications.values())
        notification.close();
      this.notifications.clear();
    }
    this.render();
  }
  render() {
    const available =
      typeof Notification !== "undefined" && window.isSecureContext;
    this.button.disabled = !available || !this.connected;
    this.button.textContent = this.enabled
      ? "Disable desktop notifications"
      : "Enable desktop notifications";
    const count = [...this.states.values()].filter(
      (state) => state.open && state.needs_answer,
    ).length;
    this.summary.textContent = !this.connected
      ? "Session attention updates disconnected."
      : count
        ? `${count} session${count === 1 ? " needs" : "s need"} an answer.`
        : "No sessions need an answer.";
    if (available && Notification.permission === "denied")
      this.summary.textContent +=
        " Desktop notifications are blocked in this browser.";
  }
}
