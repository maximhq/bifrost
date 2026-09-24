// Case driver for the Warp e2e collection (collections/bifrost-v1-warp.postman_collection.json).
//
// createWarpLib is embedded in the generated collection as source text (see
// build-warp-collection.mjs) and evaluated inside each request's scripts, so it
// must stay self-contained: no imports, no references to anything outside its
// own body, and only what the Postman sandbox offers (pm, JSON, RegExp).
//
// One Postman request is one case. A case can take several HTTP round trips:
//   - Warp may answer with an ask_user question instead of an answer. The driver
//     picks an option (hint first, as the dashboard does), appends the question
//     turn with question:true plus the reply, and re-sends - at most twice,
//     matching Warp's own two-questions-in-a-row cap.
//   - Warp's answers come from a live model, so a case whose checks miss is
//     re-run once from scratch before it fails. Only the terminal attempt
//     records a pm.test, so newman's exit code reflects the real outcome.
// Follow-up questions ("Now break that down by provider") share a chain: each
// case in a chain starts from the history the previous one ended with.

export function createWarpLib(pm) {
  var STATE_KEY = "__warp_state";
  var MAX_RETRIES = 1;
  var MAX_QUESTIONS = 2;
  // Tools that only discover what exists. A reply backed by these alone rests
  // on no data - a refusal should call nothing else.
  var DISCOVERY_TOOLS = ["describe_filter_space", "ask_user"];
  // Options the driver prefers when Warp asks, most specific first: whole
  // deployment for scope (nobody is identified, so there is no default), then a
  // week for the window, which is what every seeded fact is measured over.
  var DEFAULT_PREFER = [
    "whole deployment|entire deployment|all traffic|everyone|all teams|everything",
    "-7d|7 days|this week|last week",
    "-24h|24 hours",
  ];

  function readState() {
    var raw = pm.collectionVariables.get(STATE_KEY);
    if (!raw) return null;
    try {
      return JSON.parse(raw);
    } catch (e) {
      return null;
    }
  }

  function saveState(state) {
    pm.collectionVariables.set(STATE_KEY, state ? JSON.stringify(state) : "");
  }

  function chainKey(name) {
    return "__warp_chain_" + name;
  }

  function readChain(name) {
    var raw = pm.collectionVariables.get(chainKey(name));
    if (!raw) return null;
    try {
      return JSON.parse(raw);
    } catch (e) {
      return null;
    }
  }

  function freshState(c, attempt) {
    var messages = [];
    var conversationId = pm.variables.replaceIn("{{$guid}}");
    if (c.chain && !c.chainStart) {
      var chain = readChain(c.chain);
      if (chain) {
        messages = chain.messages.slice();
        conversationId = chain.conversation_id;
      }
    }
    if (c.history) {
      for (var i = 0; i < c.history.length; i++) {
        var turn = c.history[i];
        var copy = { role: turn.role, content: pm.variables.replaceIn(turn.content) };
        if (turn.question) copy.question = true;
        messages.push(copy);
      }
    }
    messages.push({ role: "user", content: pm.variables.replaceIn(c.ask) });
    return {
      name: pm.info.requestName,
      attempt: attempt,
      asked: 0,
      messages: messages,
      conversation_id: conversationId,
    };
  }

  // pickAnswer replies to an ask_user question the way a person clicking the
  // dashboard would: the option's hint when it has one, else its label. An
  // option the person's own question already named wins - asked "what did
  // Platform Engineering spend", nobody then picks "Whole deployment". With no
  // whole-deployment option on a scope question, they type it instead, as the
  // question's free-text escape allows, rather than taking whatever came first.
  function pickAnswer(question, prefer, asked) {
    var options = (question && question.options) || [];
    var said = String(asked || "").toLowerCase();
    for (var n = 0; n < options.length; n++) {
      var label = String(options[n].label || "").toLowerCase();
      if (label.length >= 3 && said.indexOf(label) >= 0) return options[n].hint || options[n].label;
    }
    var patterns = (prefer || []).concat(DEFAULT_PREFER);
    for (var p = 0; p < patterns.length; p++) {
      var re = new RegExp(patterns[p], "i");
      for (var i = 0; i < options.length; i++) {
        var o = options[i];
        if (re.test(o.label || "") || re.test(o.hint || "")) return o.hint || o.label;
      }
    }
    var scopeQuestion = (question && question.kind === "scope") || /whose|scope/i.test((question && question.question) || "");
    if (scopeQuestion && (!options.length || question.allow_other)) return "The whole deployment.";
    if (options.length) return options[0].hint || options[0].label;
    return "The whole deployment, over the last 7 days.";
  }

  // Patterns may name run-specific values ({{warp_slowest_id}}), resolved here
  // rather than at build time since they only exist once the seeder has run.
  function pattern(p) {
    return new RegExp(pm.variables.replaceIn(p), "i");
  }

  function stripCode(text) {
    return String(text || "")
      .replace(/```[\s\S]*?```/g, "")
      .replace(/`[^`]*`/g, "");
  }

  // evaluate returns the list of expectations a JSON-mode response missed.
  function evaluate(body, expect) {
    var e = expect || {};
    var failures = [];
    if (body.error) {
      failures.push("error " + body.error.code + ": " + body.error.message);
      return failures;
    }
    var calls = (body.tool_calls || []).filter(function (t) {
      return !t.failed;
    });
    var names = calls.map(function (t) {
      return t.name;
    });

    if (e.question === "required") {
      if (!body.question) failures.push("expected an ask_user question, got an answer");
      else if (!body.question.options || body.question.options.length < 2)
        failures.push("question carried fewer than 2 options");
      if (body.finish_reason !== "question") failures.push("finish_reason " + body.finish_reason + ", want question");
      return failures;
    }
    if (e.question === "forbidden" && body.question) failures.push("asked a question: " + body.question.question);

    var finish = e.finish || ["stop", "partial"];
    if (finish.indexOf(body.finish_reason) < 0) failures.push("finish_reason " + body.finish_reason + ", want one of " + finish.join("/"));

    // A "required" question case has already returned above; every case that
    // reaches here, "forbidden" included, must have said something.
    var answer = String(body.answer || "");
    if (!answer.trim()) failures.push("empty answer");

    if (typeof e.maxToolCalls === "number" && (body.tool_calls || []).length > e.maxToolCalls)
      failures.push("made " + (body.tool_calls || []).length + " tool calls, want at most " + e.maxToolCalls);
    if (e.toolsAny && !e.toolsAny.some(function (t) { return names.indexOf(t) >= 0; }))
      failures.push("called none of " + e.toolsAny.join(", ") + " (called: " + (names.join(", ") || "nothing") + ")");
    (e.toolsAll || []).forEach(function (t) {
      if (names.indexOf(t) < 0) failures.push("did not call " + t + " (called: " + (names.join(", ") || "nothing") + ")");
    });
    if (e.noDataTools) {
      var data = names.filter(function (n) { return DISCOVERY_TOOLS.indexOf(n) < 0; });
      if (data.length) failures.push("queried data for an out-of-scope question: " + data.join(", "));
    }
    (e.argsMatch || []).forEach(function (m) {
      var re = pattern(m.pattern);
      var hit = calls.some(function (t) {
        return (!m.tool || t.name === m.tool) && re.test(t.arguments || "");
      });
      if (!hit) failures.push("no " + (m.tool || "tool") + " call with arguments matching /" + m.pattern + "/");
    });

    (e.answerAll || []).forEach(function (p) {
      if (!pattern(p).test(answer)) failures.push("answer lacks /" + p + "/");
    });
    if (e.answerAny && !e.answerAny.some(function (p) { return pattern(p).test(answer); }))
      failures.push("answer matches none of " + e.answerAny.map(function (p) { return "/" + p + "/"; }).join(" "));
    (e.answerNone || []).forEach(function (p) {
      if (pattern(p).test(answer)) failures.push("answer contains /" + p + "/");
    });
    if (e.answerNoneOutsideCode) {
      var prose = stripCode(answer);
      e.answerNoneOutsideCode.forEach(function (p) {
        if (pattern(p).test(prose)) failures.push("answer repeats /" + p + "/ outside inline code");
      });
    }
    if (e.maxAnswerChars && answer.length > e.maxAnswerChars)
      failures.push("answer is " + answer.length + " chars, want at most " + e.maxAnswerChars);
    return failures;
  }

  function prerequest(c) {
    var state = readState();
    if (!state || state.name !== pm.info.requestName) {
      state = freshState(c, 0);
      saveState(state);
    }
    pm.request.body.update({
      mode: "raw",
      raw: JSON.stringify({
        messages: state.messages,
        conversation_id: state.conversation_id,
        stream: false,
        timezone: "UTC",
      }),
      options: { raw: { language: "json" } },
    });
  }

  function summary(body) {
    if (!body) return "";
    var tools = (body.tool_calls || []).map(function (t) {
      return t.name + (t.failed ? "(failed)" : "");
    });
    return " | tools: [" + tools.join(", ") + "] | finish: " + body.finish_reason + " | answer: " + String(body.answer || "").slice(0, 600);
  }

  function test(c) {
    var state = readState() || freshState(c, 0);
    var body = null;
    var failures = [];
    if (pm.response.code !== 200) {
      failures.push("status " + pm.response.code + ": " + pm.response.text().slice(0, 500));
    } else {
      try {
        body = pm.response.json();
      } catch (err) {
        failures.push("response is not JSON: " + pm.response.text().slice(0, 300));
      }
    }

    if (body && body.question && (c.expect || {}).question !== "required" && (c.expect || {}).question !== "forbidden" && state.asked < MAX_QUESTIONS) {
      state.messages.push({ role: "assistant", content: body.question.question, question: true });
      state.messages.push({ role: "user", content: pickAnswer(body.question, c.prefer, pm.variables.replaceIn(c.ask)) });
      state.asked += 1;
      saveState(state);
      pm.execution.setNextRequest(pm.info.requestName);
      return;
    }

    if (body) failures = failures.concat(evaluate(body, c.expect));

    if (failures.length && state.attempt < MAX_RETRIES) {
      console.log("RETRY " + pm.info.requestName + ": " + failures.join("; ") + summary(body));
      saveState(freshState(c, state.attempt + 1));
      pm.execution.setNextRequest(pm.info.requestName);
      return;
    }

    pm.test(c.name, function () {
      if (failures.length) throw new Error(failures.join("; ") + summary(body));
    });

    // The chain carries on from whatever this case ended with, answered or not,
    // so one miss does not strip the context every later follow-up relies on.
    if (c.chain && body) {
      var ended = state.messages.slice();
      if (body.question) ended.push({ role: "assistant", content: body.question.question, question: true });
      else if (body.answer) ended.push({ role: "assistant", content: body.answer });
      pm.collectionVariables.set(chainKey(c.chain), JSON.stringify({ messages: ended, conversation_id: state.conversation_id }));
    }
    saveState(null);
  }

  return { prerequest: prerequest, test: test, evaluate: evaluate, pickAnswer: pickAnswer, stripCode: stripCode };
}
