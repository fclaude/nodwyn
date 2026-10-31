// The model picker (ADR 41): which model a task talks to and how hard it
// thinks, as two selects. The New task window and a conversation share it, so
// both offer the same models, the same efforts for each, and say the same
// things about a model that cannot be used. What it offers comes from the
// daemon (settings.read's choices, models.status's readiness): it is never an
// authority, and it never picks a model for a person. A selection that can no
// longer be had is shown as it is, with why, not replaced by another.
import { h } from "./dom.js";

// Models in these states cannot be asked at all, whatever the provider does;
// "unreachable" may pass, so it only warns.
const blocking = new Set(["needs_key", "locked", "unbound", "unconfigured", "missing"]);
// stateWords is how every page says what models.status reported of a model or
// a provider, so one state reads the same in the sidebar, the picker and
// Settings.
export const stateWords = { ready: "ready", missing: "model not installed", unreachable: "not answering", unconfigured: "not set up", needs_key: "needs its key", locked: "keychain locked", unbound: "key not allowed" };

export const choicesOf = (desk) => desk.settings?.choices || [];
export const choiceNamed = (desk, name) => choicesOf(desk).find((c) => c.name === name) || null;
export const statusOf = (desk, name) => desk.models?.find((m) => m.name === name) || null;

// effortLabel is how an effort word reads: OpenAI's xhigh as "Extra high",
// any other word capitalized.
export const effortLabel = (effort) => (effort === "xhigh" ? "Extra high" : effort ? effort[0].toUpperCase() + effort.slice(1) : "Provider default");

// defaultSelection is the model and effort a new task starts on: the default
// model of Settings, with its default effort.
export function defaultSelection(desk) {
  const c = choicesOf(desk).find((c) => c.default);
  return c ? { name: c.name, effort: c.effort || "" } : null;
}

// carryEffort is the effort a model is moved to: the one it had if the new
// model takes it too, else the new model's own default.
export function carryEffort(choice, effort) {
  return effort && (choice.efforts || []).includes(effort) ? effort : choice.effort || "";
}

// choiceFor is the listed choice a selection names. A conversation says which
// model it talks to and where (session.info), and is matched by that: a
// choice's name can change meaning under it, since a [models.NAME] entry that
// becomes the default is listed once, as "control" (ModelChoices), and
// "control" itself is whichever model is the default. A choice kept for the
// next task names no model of its own, so its name is matched, and a name the
// choices do not list is looked up in the daemon's registry (models.status),
// which holds every entry: one listed under another name is not gone.
function choiceFor(desk, selection) {
  if (!selection?.name) return null;
  const named = choiceNamed(desk, selection.name);
  const known = selection.model ? { model: selection.model, server: selection.server } : statusOf(desk, selection.name);
  if (!known?.model) return named;
  const server = known.server || "";
  if (named && named.model === known.model && (named.server || "") === server) return named;
  return choicesOf(desk).find((c) => c.model === known.model && (c.server || "") === server) || named;
}

// choiceOf is what a selection names: a listed model, or, for a conversation
// on a model that is not in the list, what the daemon says of it. The daemon's
// word on a conversation's own model carries the efforts it takes, which a
// choice matched by the model it names may not when two entries name one model.
function choiceOf(desk, selection, extra) {
  if (!selection) return { listed: null, choice: null };
  const listed = choiceFor(desk, selection);
  return { listed, choice: listed && extra ? { ...listed, ...extra } : listed || extra || null };
}

// problem says why a selection cannot be used, or may not work, in the words
// a person needs: what is wrong and where it is put right. It is null for one
// that can be used, and while the daemon has not yet said (the readiness
// check is slow when a server does not answer).
export function problem(desk, selection, extra) {
  if (!selection) return { kind: "error", text: "No model is set up yet.", fix: "Set one up" };
  const { listed, choice } = choiceOf(desk, selection, extra);
  if (selection.name && !listed) return { kind: "error", text: `${selection.name} is no longer in your list of models.`, fix: "Settings › Models" };
  if (choice && selection.effort && !(choice.efforts || []).includes(selection.effort)) return { kind: "error", text: `${choice.model} does not take the effort “${selection.effort}”.` };
  const status = listed ? statusOf(desk, listed.name) : null;
  if (status && status.state !== "ready") {
    const kind = blocking.has(status.state) ? "error" : "warn";
    return { kind, text: status.detail || `${status.model} is ${stateWords[status.state] || status.state}.`, fix: kind === "error" ? "Settings › Models" : "" };
  }
  return null;
}

// blocked says a new task cannot start on the selection.
export const blocked = (desk, selection) => problem(desk, selection)?.kind === "error";

// The judge (ADR 47): which judge a task asks is chosen beside its model, from
// the judges settings.read lists. A judge that cannot be asked does not stop a
// task: you are asked instead, so it only warns.
export const judgesOf = (desk) => desk.settings?.judges || [];
export const judgeNamed = (desk, name) => judgesOf(desk).find((j) => j.name === name) || null;
// defaultJudge is the judge new tasks start with: [judge]'s server, or "".
export const defaultJudge = (desk) => judgesOf(desk).find((j) => j.default)?.name || "";
export const judgeKinds = { jev: "Jev", openai: "OpenAI Decisions" };
// judgeStateWords is the same for a judge: a model's words, and plan, which is
// a ChatGPT plan that cannot ask the Decisions API.
export const judgeStateWords = { ready: "ready", needs_key: stateWords.needs_key, locked: stateWords.locked, unbound: stateWords.unbound, plan: "needs an API key" };
// judgeLabel names a judge as the pickers list it.
export const judgeLabel = (j) => (j ? `${j.name}${judgeKinds[j.provider] && judgeKinds[j.provider] !== j.name ? ` · ${judgeKinds[j.provider]}` : ""}` : "No judge");

// judgeProblem says why a task's judge cannot be asked, in words, or null.
export function judgeProblem(desk, name) {
  if (!name) return null;
  const j = judgeNamed(desk, name);
  if (!j) return { kind: "warn", text: `The judge ${name} is no longer set up, so you answer this task's commands.`, fix: "Settings › Judge" };
  if (j.state !== "ready") return { kind: "warn", text: `${j.detail || `${j.name} ${judgeStateWords[j.state] || j.state}`}. Until it can be asked, you answer the commands.`, fix: "Settings › Judge" };
  return null;
}

// Who answers a task's commands (ADR 35, amended 2026-10-30) is chosen beside
// its model: approval.commands is what a task starts with, and each task may
// have its own, as it opens or since. It also says whether the judge is
// asked at all: asking the person and allowing all ask it nothing, its plan
// score included.
export const commandsChoices = [["judge", "Judge + me"], ["judge-auto", "Judge only"], ["ask", "Ask me"], ["allow", "Allow all"]];
// commandsWords says what each answer does, for the select's title.
const commandsWords = { judge: "The judge answers each command; you answer what it refuses.", "judge-auto": "The judge answers each command; what it refuses is refused.", ask: "You answer each command. No judge is asked.", allow: "Commands run without asking anyone. No judge is asked." };
export const judged = (mode) => mode === "judge" || mode === "judge-auto";
export const commandsLabel = (mode) => commandsChoices.find(([value]) => value === mode)?.[1] || mode;
// defaultCommands is approval.commands as Settings has it.
export const defaultCommands = (desk) => desk.settings?.values?.["approval.commands"]?.value || "judge";

const label = (text, control) => h("label", { class: "mp-field" }, h("span", {}, text), control);

export class ModelPicker {
  constructor(id, compact = false) {
    this.id = id;
    this.sig = "";
    this.props = null;
    this.model = h("select", { class: "mp-select", id: `${id}-model`, "aria-label": "Model", onchange: () => this.pickModel() });
    this.effort = h("select", { class: "mp-select", id: `${id}-effort`, "aria-label": "Effort", onchange: () => this.pickEffort() });
    this.judge = h("select", { class: "mp-select", id: `${id}-judge`, "aria-label": "Judge", onchange: () => this.props?.onJudge?.(this.judge.value) });
    this.effortField = label("Effort", this.effort);
    this.judgeField = label("Judge", this.judge);
    this.judgeField.hidden = true;
    this.commands = h("select", { class: "mp-select", id: `${id}-commands`, "aria-label": "Approvals", onchange: () => this.pickCommands() });
    this.commandsField = label("Approvals", this.commands);
    this.commandsField.hidden = true;
    this.note = h("div", { class: "mp-notes" });
    const controls = (this.controls = h("div", { class: "mp-controls" }, label("Model", this.model), this.effortField, this.commandsField, this.judgeField));
    if (compact) {
      this.summaryModel = h("span", { class: "mp-summary-model" });
      this.summaryEffort = h("span", { class: "mp-summary-effort" });
      this.summaryJudge = h("span", { class: "mp-summary-effort mp-summary-judge" });
      this.summaryJudge.hidden = true;
      this.summary = h("summary", { class: "mp-summary" }, this.summaryModel, this.summaryEffort, this.summaryJudge);
      this.details = h("details", { class: "mp-details" }, this.summary, controls);
    }
    this.el = h("div", { class: "model-picker", id, role: "group", "aria-label": "Model and effort" }, this.details || controls, this.note);
  }

  // update draws the picker from props: desk; selection ({name, server?, model?,
  // effort}); extra (what the daemon says of a conversation's own model:
  // {model, efforts, effort}); unavailable (the conversation has no model it
  // can use: none is shown as chosen, and one is asked for); disabled; notes
  // (more lines to say, {kind, text}); onPick(selection, reset), called when a
  // person picks, with reset saying the effort was changed because the new
  // model does not take it. With judge (the server of the judge chosen, ""
  // for none) it also shows the Judge select (ADR 47), calling onJudge(name)
  // when a person picks one; judgeDisabled turns that select off alone. With
  // commands (approval.commands' value the task has) it shows who answers the
  // task's commands, calling onCommands(mode); commandsDisabled turns it off
  // alone. It
  // redraws only what changed, so a list being open is not closed by the
  // desk refreshing under it.
  update(props) {
    this.props = props;
    const { desk, selection, extra, disabled = false, notes = [], unavailable = false, judge, judgeDisabled = false, judgeHint = "", commands, commandsDisabled = false } = props;
    const choices = choicesOf(desk);
    const states = choices.map((c) => statusOf(desk, c.name)?.state || "");
    const found = unavailable ? null : problem(desk, selection, extra);
    const judges = judge === undefined ? null : judgesOf(desk);
    // Beside an answer that asks no judge, the judge is not asked, so what
    // is wrong with it does not matter.
    const judgeFound = judge === undefined || (commands !== undefined && !judged(commands)) ? null : judgeProblem(desk, judge);
    const sig = JSON.stringify([choices, states, selection, extra, disabled, notes, found, unavailable, judges, judge, judgeDisabled, judgeHint, commands, commandsDisabled, defaultCommands(desk)]);
    if (sig === this.sig) return;
    this.sig = sig;
    const { listed, choice } = choiceOf(desk, selection, extra);
    if (this.details) {
      const model = unavailable ? "Choose a model…" : choice?.model || selection?.model || selection?.name || "No model";
      const effort = selection && choice?.efforts?.length ? effortLabel(selection.effort) : "";
      this.summaryModel.textContent = model;
      this.summaryEffort.textContent = effort;
      this.summaryEffort.hidden = !effort;
      const judgeWords = judge === undefined || (commands !== undefined && !judged(commands)) ? "" : judge ? `judged by ${judge}` : "no judge";
      const said = [commands === undefined ? "" : commandsLabel(commands).toLowerCase(), judgeWords].filter(Boolean).join(" · ");
      this.summaryJudge.textContent = said;
      this.summaryJudge.hidden = !said;
      this.summary.title = said ? "Change model, effort and approvals" : "Change model and effort";
      this.summary.setAttribute("aria-label", `Model: ${model}${effort ? `, effort: ${effort}` : ""}${said ? `, ${said}` : ""}. ${this.summary.title}`);
      // A task that cannot run should offer the choice, rather than hide its remedy.
      if (unavailable) this.details.open = true;
    }

    const groups = new Map();
    choices.forEach((c, i) => {
      const where = c.server ? `${c.server} · ${c.host}` : c.host;
      if (!groups.has(where)) groups.set(where, []);
      const note = states[i] && states[i] !== "ready" ? ` — ${stateWords[states[i]] || states[i]}` : "";
      groups.get(where).push(h("option", { value: c.name }, `${c.model}${c.default ? " (default)" : ""}${note}`));
    });
    const options = [...groups].map(([where, list]) => h("optgroup", { label: where }, list));
    if (selection && !listed) options.push(h("option", { value: "~" }, `${selection.model || selection.name}${selection.name ? " (no longer in your list)" : " (not in your list)"}`));
    if (!selection) options.unshift(h("option", { value: "" }, unavailable ? "Choose a model…" : "No model"));
    this.model.replaceChildren(...options);
    this.model.value = listed ? listed.name : selection ? "~" : "";
    this.model.disabled = disabled || !choices.length;

    const efforts = choice?.efforts || [];
    const own = choice?.effort ?? "";
    const levels = efforts.map((e) => h("option", { value: e }, effortLabel(e)));
    if (!selection) {
      this.effort.replaceChildren(h("option", { value: "" }, "—"));
      this.effort.disabled = true;
      this.effort.title = "";
    } else if (!efforts.length) {
      // A model with no effort setting has nothing to choose: the field is
      // left out, as it would only read "Not configurable".
      this.effort.replaceChildren(h("option", { value: "" }, "Not configurable"));
      this.effort.value = "";
      this.effort.disabled = true;
      this.effort.title = "This model takes no effort setting, so its provider's default applies. Declare the efforts it takes in Settings › Models.";
    } else {
      // A model whose own default is an effort always asks one; one with no
      // default can also be left to its provider.
      const opts = [...(own === "" ? [h("option", { value: "" }, effortLabel(""))] : []), ...levels];
      if (selection?.effort && !efforts.includes(selection.effort)) opts.push(h("option", { value: selection.effort }, `${effortLabel(selection.effort)} (not offered)`));
      this.effort.replaceChildren(...opts);
      this.effort.value = selection?.effort ?? "";
      this.effort.disabled = disabled;
      this.effort.title = "";
    }

    this.effortField.hidden = !!selection && !efforts.length;
    // The judge's select lists no judge, then each judge the daemon lists,
    // and a chosen one it no longer lists as it is, so what is shown is
    // what the task has. Beside approvals, it shows only when they ask a
    // judge and there is a choice of one to make, and "no judge" is said by
    // the approvals rather than offered twice.
    const pickable = commands === undefined || (judged(commands) && judges !== null && (judges.length > 1 || !judges.some((j) => j.name === judge)));
    this.judgeField.hidden = judges === null || !pickable;
    if (judges !== null) {
      const none = commands === undefined ? "No judge" : "Choose a judge…";
      const opts = [...(commands === undefined || !judge ? [h("option", { value: "" }, none)] : []), ...judges.map((j) => h("option", { value: j.name }, `${judgeLabel(j)}${j.default ? " (default)" : ""}${j.state !== "ready" ? ` — ${judgeStateWords[j.state] || j.state}` : ""}`))];
      if (judge && !judges.some((j) => j.name === judge)) opts.push(h("option", { value: judge }, `${judge} (no longer set up)`));
      this.judge.replaceChildren(...opts);
      this.judge.value = judge || "";
      this.judge.disabled = disabled || judgeDisabled;
      this.judge.title = judgeHint;
    }

    // Who answers commands lists the four answers, the one Settings starts
    // tasks with marked.
    this.commandsField.hidden = commands === undefined;
    if (commands !== undefined) {
      const fallback = defaultCommands(desk);
      this.commands.replaceChildren(...commandsChoices.map(([value, text]) => h("option", { value }, `${text}${value === fallback ? " (default)" : ""}`)));
      this.commands.value = commands;
      this.commands.disabled = disabled || commandsDisabled;
      this.commands.title = commandsWords[commands] || "";
    }

    // The judge alone, with none set up, refuses every command, so it says
    // so; the judge then the person, the default, asks the person, which is
    // what they expect, so it does not.
    const noJudge = commands === "judge-auto" && !judgesOf(desk).length ? { kind: "warn", text: "No judge is set up, so every command is refused.", fix: "Settings › Judge" } : null;
    const lines = [...(found ? [{ kind: found.kind, text: found.text, fix: found.fix }] : []), ...(judgeFound ? [judgeFound] : []), ...(noJudge ? [noJudge] : []), ...notes];
    this.note.replaceChildren(
      ...lines.map((line) =>
        h(
          "p",
          { class: `mp-note ${line.kind || ""}`, role: line.kind === "error" ? "alert" : null },
          line.text,
          line.fix ? [" ", h("a", { href: line.href || (line.fix.startsWith("Set") ? "#/" : line.fix.includes("Judge") ? "#/settings/judge" : "#/settings/models") }, line.fix)] : null,
        ),
      ),
    );
    this.note.hidden = !lines.length;
  }

  // pickCommands chooses who answers the task's commands. Choosing one that
  // asks a judge for a task with none chooses a judge too: one that can be
  // asked, the default first, else one that cannot yet, whose note then says
  // why, so the answer picked is the one the task gets once it can.
  pickCommands() {
    const { desk, judge, onCommands, onJudge } = this.props || {};
    const mode = this.commands.value;
    onCommands?.(mode);
    if (!judged(mode) || judge !== "" || !onJudge) return;
    const judges = judgesOf(desk);
    const ready = judges.filter((j) => j.state === "ready");
    const pick = ready.find((j) => j.default) || ready[0] || judges.find((j) => j.default) || judges[0];
    if (pick) onJudge(pick.name);
  }

  pickModel() {
    const { desk, selection, onPick } = this.props;
    const choice = choiceNamed(desk, this.model.value);
    if (!choice || !onPick) return;
    const effort = carryEffort(choice, selection?.effort);
    onPick({ name: choice.name, effort }, !!selection?.effort && effort !== selection.effort);
  }

  pickEffort() {
    const { selection, onPick } = this.props;
    if (!selection || !onPick) return;
    onPick({ ...selection, effort: this.effort.value }, false);
  }
}
