// Generates the "Direct-Provider vs Bifrost Token Parity Matrix" folder consumed by
// augment-provider-harness.mjs. Design rationale, tolerance-band reasoning, and per-provider
// doc citations for the SKIP matrix live in /Users/akshay/.claude/plans/humble-coalescing-marble.md.
//
// Each (backend, modality) cell that isn't SKIPped runs the SAME 3-round conversation twice:
// once straight against the provider's native API ("direct" leg), once through Bifrost's own
// drop-in integration route for that same provider - /openai, /anthropic, /genai, /bedrock -
// in that provider's native wire shape ("bifrost" leg). Both legs receive identical fixed turns;
// assistant replies are real (independently sampled), which is what makes the token-usage
// comparison meaningful. State passes between rounds via pm.collectionVariables, following the
// same pattern already used elsewhere in provider-harness.json (see e.g. the
// anthropicReasoningReplayInput39 / openaiReasoningReplayContent5186 variables): each round's
// test script pre-computes the NEXT round's full request body (as a JSON string) and stores it,
// so the following round's raw body is a pure `{{var}}` drop-in with no pre-request scripting.
import {
  IMAGE_JPEG_BASE64,
  DOCUMENT_PDF_BASE64,
  AUDIO_WAV_BASE64,
  VIDEO_MP4_BASE64,
} from "./token-parity-fixtures.mjs";

// One relative tolerance used for prompt/cached (checked against ROUND 1 data specifically -
// see the round-1 capture comment in buildLegItems for why: rounds 2-3's prompt/cached include
// each leg's own independently-sampled prior assistant replies, so they're not a clean
// comparison no matter how wide the band) and, with an added absolute floor, for completion
// (checked against round 3's cumulative data, since that's the point of running 3 rounds).
const TOLERANCE_PCT = 20;
// Rescues short free-text answers from false positives: a 5-9 token difference in a one-
// sentence reply is normal independent-sampling variance, not drift, but registers as a large
// percentage at small absolute magnitudes (confirmed against real run data - this was the
// single biggest source of FAILs that weren't actual bugs). Passing on EITHER the relative band
// OR this floor, whichever is looser, doesn't weaken detection of genuinely large swings (they
// fail both checks), it just stops small numbers from manufacturing noise on their own.
//
// Raised from 10 after gemini/audio r3 reported direct=15 bifrost=30 while the two legs' PROMPT
// counts sat one token apart (6585 vs 6584) - the conversation reached the model identically on
// both legs and the model simply answered at double length on one, restating its earlier turns
// instead of obeying "reply with ONLY the exact text". One extra volunteered sentence is the
// natural unit of this noise and costs about 15 tokens, so a floor of 10 cannot absorb it. Real
// gateway drift in completion counts does not look like this: it is systematic across a
// backend's cells or it is zero, and either still fails both checks at 24.
const COMPLETION_ABS_FLOOR = 24;

// Deterministic ~5000-token reference block (274 numbered, templated facts about a fictional
// research station - never a real-world topic a model might "correct" or paraphrase instead of
// quoting verbatim). Two goals per your request: (1) push every round's prompt well into the
// 3k-7k token band, so a 1-2 token tokenizer-boundary difference stops registering as visible
// percentage noise, and (2) make completions predictable - each question below has exactly one
// verifiable correct answer (quote a specific numbered fact, or count them), not an open-ended
// "summarize" that two independent samples would phrase differently. Regenerate via
// large_context.txt / tiktoken if this ever needs resizing; the exact fact text matters (round
// questions reference Fact #1 and Fact #274 by their literal content), so don't hand-edit it.
const LARGE_CONTEXT = "Fact #1: The station's primary reactor operates at a core temperature of 1 degrees.\nFact #2: Module 20 of the station houses the atmospheric processing unit.\nFact #3: The station's solar array generates 153 kilowatts during peak exposure.\nFact #4: Corridor 184 connects the habitation ring to the command deck.\nFact #5: The water reclamation system in bay 401 recycles 401 liters per cycle.\nFact #6: Sensor array 120 monitors radiation levels along the station's northern hull.\nFact #7: The station's library holds 853 archived research volumes in section 853.\nFact #8: Airlock 584 was last serviced during maintenance cycle 584.\nFact #9: The greenhouse dome cultivates 501 distinct crop varieties in tier 501.\nFact #10: Backup generator 820 provides 820 hours of emergency power reserve.\nFact #11: The station's primary reactor operates at a core temperature of 353 degrees.\nFact #12: Module 684 of the station houses the atmospheric processing unit.\nFact #13: The station's solar array generates 301 kilowatts during peak exposure.\nFact #14: Corridor 320 connects the habitation ring to the command deck.\nFact #15: The water reclamation system in bay 453 recycles 453 liters per cycle.\nFact #16: Sensor array 484 monitors radiation levels along the station's northern hull.\nFact #17: The station's library holds 701 archived research volumes in section 701.\nFact #18: Airlock 420 was last serviced during maintenance cycle 420.\nFact #19: The greenhouse dome cultivates 253 distinct crop varieties in tier 253.\nFact #20: Backup generator 884 provides 884 hours of emergency power reserve.\nFact #21: The station's primary reactor operates at a core temperature of 801 degrees.\nFact #22: Module 220 of the station houses the atmospheric processing unit.\nFact #23: The station's solar array generates 653 kilowatts during peak exposure.\nFact #24: Corridor 84 connects the habitation ring to the command deck.\nFact #25: The water reclamation system in bay 601 recycles 601 liters per cycle.\nFact #26: Sensor array 620 monitors radiation levels along the station's northern hull.\nFact #27: The station's library holds 753 archived research volumes in section 753.\nFact #28: Airlock 784 was last serviced during maintenance cycle 784.\nFact #29: The greenhouse dome cultivates 101 distinct crop varieties in tier 101.\nFact #30: Backup generator 720 provides 720 hours of emergency power reserve.\nFact #31: The station's primary reactor operates at a core temperature of 553 degrees.\nFact #32: Module 284 of the station houses the atmospheric processing unit.\nFact #33: The station's solar array generates 201 kilowatts during peak exposure.\nFact #34: Corridor 520 connects the habitation ring to the command deck.\nFact #35: The water reclamation system in bay 53 recycles 53 liters per cycle.\nFact #36: Sensor array 384 monitors radiation levels along the station's northern hull.\nFact #37: The station's library holds 901 archived research volumes in section 901.\nFact #38: Airlock 20 was last serviced during maintenance cycle 20.\nFact #39: The greenhouse dome cultivates 153 distinct crop varieties in tier 153.\nFact #40: Backup generator 184 provides 184 hours of emergency power reserve.\nFact #41: The station's primary reactor operates at a core temperature of 401 degrees.\nFact #42: Module 120 of the station houses the atmospheric processing unit.\nFact #43: The station's solar array generates 853 kilowatts during peak exposure.\nFact #44: Corridor 584 connects the habitation ring to the command deck.\nFact #45: The water reclamation system in bay 501 recycles 501 liters per cycle.\nFact #46: Sensor array 820 monitors radiation levels along the station's northern hull.\nFact #47: The station's library holds 353 archived research volumes in section 353.\nFact #48: Airlock 684 was last serviced during maintenance cycle 684.\nFact #49: The greenhouse dome cultivates 301 distinct crop varieties in tier 301.\nFact #50: Backup generator 320 provides 320 hours of emergency power reserve.\nFact #51: The station's primary reactor operates at a core temperature of 453 degrees.\nFact #52: Module 484 of the station houses the atmospheric processing unit.\nFact #53: The station's solar array generates 701 kilowatts during peak exposure.\nFact #54: Corridor 420 connects the habitation ring to the command deck.\nFact #55: The water reclamation system in bay 253 recycles 253 liters per cycle.\nFact #56: Sensor array 884 monitors radiation levels along the station's northern hull.\nFact #57: The station's library holds 801 archived research volumes in section 801.\nFact #58: Airlock 220 was last serviced during maintenance cycle 220.\nFact #59: The greenhouse dome cultivates 653 distinct crop varieties in tier 653.\nFact #60: Backup generator 84 provides 84 hours of emergency power reserve.\nFact #61: The station's primary reactor operates at a core temperature of 601 degrees.\nFact #62: Module 620 of the station houses the atmospheric processing unit.\nFact #63: The station's solar array generates 753 kilowatts during peak exposure.\nFact #64: Corridor 784 connects the habitation ring to the command deck.\nFact #65: The water reclamation system in bay 101 recycles 101 liters per cycle.\nFact #66: Sensor array 720 monitors radiation levels along the station's northern hull.\nFact #67: The station's library holds 553 archived research volumes in section 553.\nFact #68: Airlock 284 was last serviced during maintenance cycle 284.\nFact #69: The greenhouse dome cultivates 201 distinct crop varieties in tier 201.\nFact #70: Backup generator 520 provides 520 hours of emergency power reserve.\nFact #71: The station's primary reactor operates at a core temperature of 53 degrees.\nFact #72: Module 384 of the station houses the atmospheric processing unit.\nFact #73: The station's solar array generates 901 kilowatts during peak exposure.\nFact #74: Corridor 20 connects the habitation ring to the command deck.\nFact #75: The water reclamation system in bay 153 recycles 153 liters per cycle.\nFact #76: Sensor array 184 monitors radiation levels along the station's northern hull.\nFact #77: The station's library holds 401 archived research volumes in section 401.\nFact #78: Airlock 120 was last serviced during maintenance cycle 120.\nFact #79: The greenhouse dome cultivates 853 distinct crop varieties in tier 853.\nFact #80: Backup generator 584 provides 584 hours of emergency power reserve.\nFact #81: The station's primary reactor operates at a core temperature of 501 degrees.\nFact #82: Module 820 of the station houses the atmospheric processing unit.\nFact #83: The station's solar array generates 353 kilowatts during peak exposure.\nFact #84: Corridor 684 connects the habitation ring to the command deck.\nFact #85: The water reclamation system in bay 301 recycles 301 liters per cycle.\nFact #86: Sensor array 320 monitors radiation levels along the station's northern hull.\nFact #87: The station's library holds 453 archived research volumes in section 453.\nFact #88: Airlock 484 was last serviced during maintenance cycle 484.\nFact #89: The greenhouse dome cultivates 701 distinct crop varieties in tier 701.\nFact #90: Backup generator 420 provides 420 hours of emergency power reserve.\nFact #91: The station's primary reactor operates at a core temperature of 253 degrees.\nFact #92: Module 884 of the station houses the atmospheric processing unit.\nFact #93: The station's solar array generates 801 kilowatts during peak exposure.\nFact #94: Corridor 220 connects the habitation ring to the command deck.\nFact #95: The water reclamation system in bay 653 recycles 653 liters per cycle.\nFact #96: Sensor array 84 monitors radiation levels along the station's northern hull.\nFact #97: The station's library holds 601 archived research volumes in section 601.\nFact #98: Airlock 620 was last serviced during maintenance cycle 620.\nFact #99: The greenhouse dome cultivates 753 distinct crop varieties in tier 753.\nFact #100: Backup generator 784 provides 784 hours of emergency power reserve.\nFact #101: The station's primary reactor operates at a core temperature of 101 degrees.\nFact #102: Module 720 of the station houses the atmospheric processing unit.\nFact #103: The station's solar array generates 553 kilowatts during peak exposure.\nFact #104: Corridor 284 connects the habitation ring to the command deck.\nFact #105: The water reclamation system in bay 201 recycles 201 liters per cycle.\nFact #106: Sensor array 520 monitors radiation levels along the station's northern hull.\nFact #107: The station's library holds 53 archived research volumes in section 53.\nFact #108: Airlock 384 was last serviced during maintenance cycle 384.\nFact #109: The greenhouse dome cultivates 901 distinct crop varieties in tier 901.\nFact #110: Backup generator 20 provides 20 hours of emergency power reserve.\nFact #111: The station's primary reactor operates at a core temperature of 153 degrees.\nFact #112: Module 184 of the station houses the atmospheric processing unit.\nFact #113: The station's solar array generates 401 kilowatts during peak exposure.\nFact #114: Corridor 120 connects the habitation ring to the command deck.\nFact #115: The water reclamation system in bay 853 recycles 853 liters per cycle.\nFact #116: Sensor array 584 monitors radiation levels along the station's northern hull.\nFact #117: The station's library holds 501 archived research volumes in section 501.\nFact #118: Airlock 820 was last serviced during maintenance cycle 820.\nFact #119: The greenhouse dome cultivates 353 distinct crop varieties in tier 353.\nFact #120: Backup generator 684 provides 684 hours of emergency power reserve.\nFact #121: The station's primary reactor operates at a core temperature of 301 degrees.\nFact #122: Module 320 of the station houses the atmospheric processing unit.\nFact #123: The station's solar array generates 453 kilowatts during peak exposure.\nFact #124: Corridor 484 connects the habitation ring to the command deck.\nFact #125: The water reclamation system in bay 701 recycles 701 liters per cycle.\nFact #126: Sensor array 420 monitors radiation levels along the station's northern hull.\nFact #127: The station's library holds 253 archived research volumes in section 253.\nFact #128: Airlock 884 was last serviced during maintenance cycle 884.\nFact #129: The greenhouse dome cultivates 801 distinct crop varieties in tier 801.\nFact #130: Backup generator 220 provides 220 hours of emergency power reserve.\nFact #131: The station's primary reactor operates at a core temperature of 653 degrees.\nFact #132: Module 84 of the station houses the atmospheric processing unit.\nFact #133: The station's solar array generates 601 kilowatts during peak exposure.\nFact #134: Corridor 620 connects the habitation ring to the command deck.\nFact #135: The water reclamation system in bay 753 recycles 753 liters per cycle.\nFact #136: Sensor array 784 monitors radiation levels along the station's northern hull.\nFact #137: The station's library holds 101 archived research volumes in section 101.\nFact #138: Airlock 720 was last serviced during maintenance cycle 720.\nFact #139: The greenhouse dome cultivates 553 distinct crop varieties in tier 553.\nFact #140: Backup generator 284 provides 284 hours of emergency power reserve.\nFact #141: The station's primary reactor operates at a core temperature of 201 degrees.\nFact #142: Module 520 of the station houses the atmospheric processing unit.\nFact #143: The station's solar array generates 53 kilowatts during peak exposure.\nFact #144: Corridor 384 connects the habitation ring to the command deck.\nFact #145: The water reclamation system in bay 901 recycles 901 liters per cycle.\nFact #146: Sensor array 20 monitors radiation levels along the station's northern hull.\nFact #147: The station's library holds 153 archived research volumes in section 153.\nFact #148: Airlock 184 was last serviced during maintenance cycle 184.\nFact #149: The greenhouse dome cultivates 401 distinct crop varieties in tier 401.\nFact #150: Backup generator 120 provides 120 hours of emergency power reserve.\nFact #151: The station's primary reactor operates at a core temperature of 853 degrees.\nFact #152: Module 584 of the station houses the atmospheric processing unit.\nFact #153: The station's solar array generates 501 kilowatts during peak exposure.\nFact #154: Corridor 820 connects the habitation ring to the command deck.\nFact #155: The water reclamation system in bay 353 recycles 353 liters per cycle.\nFact #156: Sensor array 684 monitors radiation levels along the station's northern hull.\nFact #157: The station's library holds 301 archived research volumes in section 301.\nFact #158: Airlock 320 was last serviced during maintenance cycle 320.\nFact #159: The greenhouse dome cultivates 453 distinct crop varieties in tier 453.\nFact #160: Backup generator 484 provides 484 hours of emergency power reserve.\nFact #161: The station's primary reactor operates at a core temperature of 701 degrees.\nFact #162: Module 420 of the station houses the atmospheric processing unit.\nFact #163: The station's solar array generates 253 kilowatts during peak exposure.\nFact #164: Corridor 884 connects the habitation ring to the command deck.\nFact #165: The water reclamation system in bay 801 recycles 801 liters per cycle.\nFact #166: Sensor array 220 monitors radiation levels along the station's northern hull.\nFact #167: The station's library holds 653 archived research volumes in section 653.\nFact #168: Airlock 84 was last serviced during maintenance cycle 84.\nFact #169: The greenhouse dome cultivates 601 distinct crop varieties in tier 601.\nFact #170: Backup generator 620 provides 620 hours of emergency power reserve.\nFact #171: The station's primary reactor operates at a core temperature of 753 degrees.\nFact #172: Module 784 of the station houses the atmospheric processing unit.\nFact #173: The station's solar array generates 101 kilowatts during peak exposure.\nFact #174: Corridor 720 connects the habitation ring to the command deck.\nFact #175: The water reclamation system in bay 553 recycles 553 liters per cycle.\nFact #176: Sensor array 284 monitors radiation levels along the station's northern hull.\nFact #177: The station's library holds 201 archived research volumes in section 201.\nFact #178: Airlock 520 was last serviced during maintenance cycle 520.\nFact #179: The greenhouse dome cultivates 53 distinct crop varieties in tier 53.\nFact #180: Backup generator 384 provides 384 hours of emergency power reserve.\nFact #181: The station's primary reactor operates at a core temperature of 901 degrees.\nFact #182: Module 20 of the station houses the atmospheric processing unit.\nFact #183: The station's solar array generates 153 kilowatts during peak exposure.\nFact #184: Corridor 184 connects the habitation ring to the command deck.\nFact #185: The water reclamation system in bay 401 recycles 401 liters per cycle.\nFact #186: Sensor array 120 monitors radiation levels along the station's northern hull.\nFact #187: The station's library holds 853 archived research volumes in section 853.\nFact #188: Airlock 584 was last serviced during maintenance cycle 584.\nFact #189: The greenhouse dome cultivates 501 distinct crop varieties in tier 501.\nFact #190: Backup generator 820 provides 820 hours of emergency power reserve.\nFact #191: The station's primary reactor operates at a core temperature of 353 degrees.\nFact #192: Module 684 of the station houses the atmospheric processing unit.\nFact #193: The station's solar array generates 301 kilowatts during peak exposure.\nFact #194: Corridor 320 connects the habitation ring to the command deck.\nFact #195: The water reclamation system in bay 453 recycles 453 liters per cycle.\nFact #196: Sensor array 484 monitors radiation levels along the station's northern hull.\nFact #197: The station's library holds 701 archived research volumes in section 701.\nFact #198: Airlock 420 was last serviced during maintenance cycle 420.\nFact #199: The greenhouse dome cultivates 253 distinct crop varieties in tier 253.\nFact #200: Backup generator 884 provides 884 hours of emergency power reserve.\nFact #201: The station's primary reactor operates at a core temperature of 801 degrees.\nFact #202: Module 220 of the station houses the atmospheric processing unit.\nFact #203: The station's solar array generates 653 kilowatts during peak exposure.\nFact #204: Corridor 84 connects the habitation ring to the command deck.\nFact #205: The water reclamation system in bay 601 recycles 601 liters per cycle.\nFact #206: Sensor array 620 monitors radiation levels along the station's northern hull.\nFact #207: The station's library holds 753 archived research volumes in section 753.\nFact #208: Airlock 784 was last serviced during maintenance cycle 784.\nFact #209: The greenhouse dome cultivates 101 distinct crop varieties in tier 101.\nFact #210: Backup generator 720 provides 720 hours of emergency power reserve.\nFact #211: The station's primary reactor operates at a core temperature of 553 degrees.\nFact #212: Module 284 of the station houses the atmospheric processing unit.\nFact #213: The station's solar array generates 201 kilowatts during peak exposure.\nFact #214: Corridor 520 connects the habitation ring to the command deck.\nFact #215: The water reclamation system in bay 53 recycles 53 liters per cycle.\nFact #216: Sensor array 384 monitors radiation levels along the station's northern hull.\nFact #217: The station's library holds 901 archived research volumes in section 901.\nFact #218: Airlock 20 was last serviced during maintenance cycle 20.\nFact #219: The greenhouse dome cultivates 153 distinct crop varieties in tier 153.\nFact #220: Backup generator 184 provides 184 hours of emergency power reserve.\nFact #221: The station's primary reactor operates at a core temperature of 401 degrees.\nFact #222: Module 120 of the station houses the atmospheric processing unit.\nFact #223: The station's solar array generates 853 kilowatts during peak exposure.\nFact #224: Corridor 584 connects the habitation ring to the command deck.\nFact #225: The water reclamation system in bay 501 recycles 501 liters per cycle.\nFact #226: Sensor array 820 monitors radiation levels along the station's northern hull.\nFact #227: The station's library holds 353 archived research volumes in section 353.\nFact #228: Airlock 684 was last serviced during maintenance cycle 684.\nFact #229: The greenhouse dome cultivates 301 distinct crop varieties in tier 301.\nFact #230: Backup generator 320 provides 320 hours of emergency power reserve.\nFact #231: The station's primary reactor operates at a core temperature of 453 degrees.\nFact #232: Module 484 of the station houses the atmospheric processing unit.\nFact #233: The station's solar array generates 701 kilowatts during peak exposure.\nFact #234: Corridor 420 connects the habitation ring to the command deck.\nFact #235: The water reclamation system in bay 253 recycles 253 liters per cycle.\nFact #236: Sensor array 884 monitors radiation levels along the station's northern hull.\nFact #237: The station's library holds 801 archived research volumes in section 801.\nFact #238: Airlock 220 was last serviced during maintenance cycle 220.\nFact #239: The greenhouse dome cultivates 653 distinct crop varieties in tier 653.\nFact #240: Backup generator 84 provides 84 hours of emergency power reserve.\nFact #241: The station's primary reactor operates at a core temperature of 601 degrees.\nFact #242: Module 620 of the station houses the atmospheric processing unit.\nFact #243: The station's solar array generates 753 kilowatts during peak exposure.\nFact #244: Corridor 784 connects the habitation ring to the command deck.\nFact #245: The water reclamation system in bay 101 recycles 101 liters per cycle.\nFact #246: Sensor array 720 monitors radiation levels along the station's northern hull.\nFact #247: The station's library holds 553 archived research volumes in section 553.\nFact #248: Airlock 284 was last serviced during maintenance cycle 284.\nFact #249: The greenhouse dome cultivates 201 distinct crop varieties in tier 201.\nFact #250: Backup generator 520 provides 520 hours of emergency power reserve.\nFact #251: The station's primary reactor operates at a core temperature of 53 degrees.\nFact #252: Module 384 of the station houses the atmospheric processing unit.\nFact #253: The station's solar array generates 901 kilowatts during peak exposure.\nFact #254: Corridor 20 connects the habitation ring to the command deck.\nFact #255: The water reclamation system in bay 153 recycles 153 liters per cycle.\nFact #256: Sensor array 184 monitors radiation levels along the station's northern hull.\nFact #257: The station's library holds 401 archived research volumes in section 401.\nFact #258: Airlock 120 was last serviced during maintenance cycle 120.\nFact #259: The greenhouse dome cultivates 853 distinct crop varieties in tier 853.\nFact #260: Backup generator 584 provides 584 hours of emergency power reserve.\nFact #261: The station's primary reactor operates at a core temperature of 501 degrees.\nFact #262: Module 820 of the station houses the atmospheric processing unit.\nFact #263: The station's solar array generates 353 kilowatts during peak exposure.\nFact #264: Corridor 684 connects the habitation ring to the command deck.\nFact #265: The water reclamation system in bay 301 recycles 301 liters per cycle.\nFact #266: Sensor array 320 monitors radiation levels along the station's northern hull.\nFact #267: The station's library holds 453 archived research volumes in section 453.\nFact #268: Airlock 484 was last serviced during maintenance cycle 484.\nFact #269: The greenhouse dome cultivates 701 distinct crop varieties in tier 701.\nFact #270: Backup generator 420 provides 420 hours of emergency power reserve.\nFact #271: The station's primary reactor operates at a core temperature of 253 degrees.\nFact #272: Module 884 of the station houses the atmospheric processing unit.\nFact #273: The station's solar array generates 801 kilowatts during peak exposure.\nFact #274: Corridor 220 connects the habitation ring to the command deck.";

// buildQ(cellId): LARGE_CONTEXT is a single static ~5000-token block shared by every cell in
// this matrix - well above both OpenAI's (1024-2048 tok, https://developers.openai.com/api/docs/
// guides/prompt-caching, "works automatically... org-scoped, not per API key") and Gemini's
// (2048 tok for 2.5 Flash, https://ai.google.dev/gemini-api/docs/caching, "implicit caching is
// enabled by default") automatic-caching thresholds. Both providers cache by exact-prefix match
// at the account/project level, with NO isolation between "logically separate" test rows that
// happen to share an account - so an unsalted shared prefix makes direct-vs-bifrost cached-token
// comparison a pure race (whichever request reaches the provider first writes the cache; ANY
// later request anywhere in the run with a matching prefix reads it, regardless of which leg or
// modality it belongs to). A short per-(backend, modality, leg) cellId prepended before
// LARGE_CONTEXT gives every leg of every row its own cold cache in round 1, so prompt/cached
// parity actually measures something (0 vs 0, or a real prompt-token divergence if one exists)
// instead of measuring HTTP request ordering. Anthropic-family backends are unaffected either way
// - Claude has no automatic caching, only explicit cache_control breakpoints (none set here).
// Fixed, short, not part of the cacheable prefix - shared across all cells (no salting needed).
const TOOL_RESULT = "27C, humid, light rain";

// Leg component of the cache salt, kept token-identical across legs on purpose: both
// values are the same length and differ only in the final digit, which every tokenizer
// here emits as a single token. The leg still gets its own cold cache (different bytes,
// different prefix hash) without the two legs of a row differing in prompt-token count.
// Never substitute the readable leg names back in -- that is the bug this replaced.
const LEG_SALT = { direct: "leg1", bifrost: "leg2" };

function buildQ(cellId) {
  const ctx = `Test cell ID: ${cellId}.\n\n${LARGE_CONTEXT}`;
  return {
    text1: `${ctx}\n\nReply with ONLY the exact text of Fact #1 above, and nothing else.`,
    text2: `${ctx}\n\nHow many facts are listed above in total? Reply with ONLY the number.`,
    text3: `${ctx}\n\nReply with ONLY the exact text of the LAST fact listed above, and nothing else.`,
    tool1: `${ctx}\n\nSeparately: what is the current weather in Lagos, Nigeria? Use the get_weather tool to look it up.`,
    tool3: `${ctx}\n\nSetting the weather aside, reply with ONLY the exact text of Fact #1 above, and nothing else.`,
    media1: (word) => `${ctx}\n\nSeparately, look at the attached ${word} and describe what it shows in exactly one short sentence (15 words or fewer).`,
    media2: `${ctx}\n\nSetting the attachment aside, how many facts are listed above in total? Reply with ONLY the number.`,
    media3: `${ctx}\n\nSetting the attachment aside, reply with ONLY the exact text of the LAST fact listed above, and nothing else.`,
  };
}

const MEDIA_WORD = { image: "image", document: "document", audio: "audio clip", video: "video clip" };

// reasoning: "off" | "on" | undefined. Only meaningful for gemini/vertex, where Gemini 2.5
// models think by DEFAULT with a dynamic (unbounded-within-budget) thinking length - that's
// inherently non-deterministic and, with a small max_tokens, can eat the whole response budget
// before any visible answer is produced (see extraParamsFor's comment for the full story).
// "off" forces thinkingBudget=0 so the other 8 modality classes measure real answer tokens with
// no reasoning noise. "on" forces an EXPLICIT fixed budget (not dynamic) on both legs so the
// reasoning-enabled comparison is still apples-to-apples rather than racing two independently-
// unpredictable thinking traces. undefined (all non-gemini/vertex backends) leaves the field
// untouched entirely, since only gemini/vertex have this default-on dynamic-thinking behavior
// in this matrix's model choices.
const MODALITIES = [
  { key: "text", label: "text (non-tool, non-streaming)", stream: false, tools: false, media: null, reasoning: "off" },
  { key: "text_streaming", label: "text (non-tool, streaming)", stream: true, tools: false, media: null, reasoning: "off" },
  { key: "tools", label: "tool calling (non-streaming)", stream: false, tools: true, media: null, reasoning: "off" },
  { key: "tools_streaming", label: "tool calling (streaming)", stream: true, tools: true, media: null, reasoning: "off" },
  { key: "image", label: "image input", stream: false, tools: false, media: "image", reasoning: "off" },
  { key: "document", label: "document (PDF) input", stream: false, tools: false, media: "document", reasoning: "off" },
  { key: "audio", label: "audio input", stream: false, tools: false, media: "audio", reasoning: "off" },
  { key: "video", label: "video input", stream: false, tools: false, media: "video", reasoning: "off" },
  { key: "reasoning_on", label: "text with reasoning enabled (non-streaming)", stream: false, tools: false, media: null, reasoning: "on" },
  { key: "reasoning_on_streaming", label: "text with reasoning enabled (streaming)", stream: true, tools: false, media: null, reasoning: "on" },
];

const REASONING_ON_BUDGET = 512;

// backend -> modality -> citation string (falsy = supported). Both legs share one SKIP matrix:
// if the provider genuinely can't do it, Bifrost can't manufacture the capability either.
const REASONING_ON_SKIP = "Only gemini/vertex use a reasoning-capable model in this matrix (gpt-4o-mini/claude-haiku-4-5 are non-reasoning); reasoning-on parity is only meaningful where reasoning is actually in play.";
const SKIP = {
  openai: {
    document:
      "Chat Completions has no PDF/file input - PDF is Responses-API-only (https://developers.openai.com/api/docs/guides/pdf-files)",
    video: "No video input support documented for Chat Completions",
    reasoning_on: REASONING_ON_SKIP,
    reasoning_on_streaming: REASONING_ON_SKIP,
  },
  anthropic: {
    audio:
      "Messages API accepts only text+image content blocks; audio input is an open feature request, not implemented (https://platform.claude.com/docs/en/build-with-claude/vision, anthropics/anthropic-sdk-python#1198)",
    video: "No video input capability documented for the Messages API",
    reasoning_on: REASONING_ON_SKIP,
    reasoning_on_streaming: REASONING_ON_SKIP,
  },
  gemini: {},
  vertex: {},
  bedrock: {
    audio:
      "AWS's supported-models-and-features table lists audio as Nova-only; no Claude-on-Bedrock audio support documented (https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference-supported-models-features.html)",
    video: "Same source lists video as Nova-only; no Claude-on-Bedrock video support documented",
    reasoning_on: REASONING_ON_SKIP,
    reasoning_on_streaming: REASONING_ON_SKIP,
  },
  // "One more model per provider" additions: Bedrock and Vertex both host more than one model
  // family, unlike the openai/anthropic/gemini (AI Studio) APIs which only ever serve their own
  // models - these two get a second backend entry testing that other family, scoped
  // conservatively to what's actually confirmed rather than guessed.
  bedrock_openai: {
    image: "gpt-oss-family capability support on Bedrock Converse not independently verified for this addition - scoped to text/tool-calling only.",
    document: "Same as image - not verified, not guessed.",
    audio: "Same as image - not verified, not guessed.",
    video: "Same as image - not verified, not guessed.",
    reasoning_on: REASONING_ON_SKIP,
    reasoning_on_streaming: REASONING_ON_SKIP,
  },
  vertex_claude: {
    audio:
      "Messages API accepts only text+image content blocks; audio input is an open feature request, not implemented (https://platform.claude.com/docs/en/build-with-claude/vision, anthropics/anthropic-sdk-python#1198)",
    video: "No video input capability documented for Claude (same as native Anthropic).",
    text_streaming:
      "Claude-on-Vertex's streaming endpoint isn't documented at https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai (only :rawPredict is confirmed) and wasn't independently verified - not guessed.",
    tools_streaming: "Same streaming-endpoint uncertainty as text_streaming.",
    reasoning_on: "Claude's own extended-thinking mechanism differs from Gemini's thinkingBudget and isn't wired up for this backend in this matrix.",
    reasoning_on_streaming: "Same as reasoning_on, plus the streaming-endpoint uncertainty above.",
  },
};

const J = (v) => JSON.stringify(v);
const execLines = (script) => script.split("\n");
const within = `function ptWithinPct(d,b,pct){if(d===0&&b===0)return true;var base=Math.max(Math.abs(d),Math.abs(b),1);return (Math.abs(d-b)/base)*100<=pct;}`;
// Resolves every {{var}} in a URL template against pm.variables at runtime, so the report shows
// the actual endpoint hit (e.g. http://localhost:8080/v1/chat/completions, or a real AWS region/
// GCP project) instead of the literal unresolved template text. Falls back to leaving a
// placeholder in place if that particular variable isn't set, rather than dropping it silently.
const resolveVars = `function ptResolveVars(s){return s.replace(/\\{\\{([a-zA-Z0-9_]+)\\}\\}/g, function(_, name){ var v = pm.variables.get(name); return (v === undefined || v === null || v === "") ? ("{{" + name + "}}") : v; });}`;

// Every item elsewhere in this collection ships `host`/`path` array breakdowns alongside
// `raw` (see e.g. the Azure drop-in item's url object) - a bare {raw: "..."} object resolves
// to an empty URL at request time (confirmed empirically: newman-report showed `URL: http://`
// for every item, both legs, before this fix landed). {{baseUrl}}-prefixed URLs keep the whole
// variable as one host segment (matching the existing convention); literal https:// URLs get
// split on "." for host and "/" for path, with an optional query string.
function urlParts(raw) {
  if (raw.startsWith("{{baseUrl}}")) {
    const rest = raw.slice("{{baseUrl}}".length);
    const [pathPart, queryPart] = rest.split("?");
    const path = pathPart.split("/").filter(Boolean);
    const query = queryPart ? queryPart.split("&").map((kv) => { const [key, value] = kv.split("="); return { key, value }; }) : undefined;
    return { raw, host: ["{{baseUrl}}"], path, ...(query ? { query } : {}) };
  }
  const m = raw.match(/^(https?):\/\/([^/]+)(\/[^?]*)?(?:\?(.*))?$/);
  if (!m) return { raw };
  const [, protocol, hostStr, pathStr, queryStr] = m;
  const host = hostStr.split(".");
  const path = (pathStr || "").split("/").filter(Boolean);
  const query = queryStr ? queryStr.split("&").map((kv) => { const [key, value] = kv.split("="); return { key, value }; }) : undefined;
  return { raw, protocol, host, path, ...(query ? { query } : {}) };
}

// ---------------------------------------------------------------------------------------------
// OpenAI-shaped turn/tool helpers - used by OpenAI's DIRECT leg and its BIFROST leg (which hits
// Bifrost's /openai drop-in route, i.e. OpenAI's own native Chat Completions wire shape).
// ---------------------------------------------------------------------------------------------
const openaiShape = {
  turnsField: "messages",
  plainTurn: (text) => ({ role: "user", content: text }),
  mediaTurn: (kind, text) => {
    const block =
      kind === "image"
        ? { type: "image_url", image_url: { url: `data:image/jpeg;base64,${IMAGE_JPEG_BASE64}` } }
        : kind === "document"
        ? { type: "file", file: { file_data: `data:application/pdf;base64,${DOCUMENT_PDF_BASE64}`, file_type: "application/pdf" } }
        : kind === "audio"
        ? { type: "input_audio", input_audio: { data: AUDIO_WAV_BASE64, format: "wav" } }
        : { type: "file", file: { file_data: `data:video/mp4;base64,${VIDEO_MP4_BASE64}`, file_type: "video/mp4" } };
    return { role: "user", content: [{ type: "text", text }, block] };
  },
  toolDef: {
    type: "function",
    function: {
      name: "get_weather",
      description: "Get the current weather for a city",
      parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
    },
  },
  toolExtraParams: { tools: [
    {
      type: "function",
      function: {
        name: "get_weather",
        description: "Get the current weather for a city",
        parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
      },
    },
  ], tool_choice: "auto" },
  toolResultTurn: (toolCallExpr) => `{role:"tool", tool_call_id: ${toolCallExpr}.id, content: ${J(TOOL_RESULT)}}`,
  extractNonStream: `
var j = pm.response.json();
var m = (j.choices && j.choices[0] && j.choices[0].message) || {};
var u = j.usage || {};
var usage = {
  prompt: u.prompt_tokens || 0,
  completion: u.completion_tokens || 0,
  cached: (u.prompt_tokens_details && u.prompt_tokens_details.cached_tokens) || 0,
  total: u.total_tokens || 0,
};
var assistantTurn = { role: "assistant" };
if (m.content) assistantTurn.content = m.content;
if (m.tool_calls) assistantTurn.tool_calls = m.tool_calls;
var toolCall = (m.tool_calls && m.tool_calls[0]) ? { id: m.tool_calls[0].id, name: m.tool_calls[0].function.name } : null;`.trim(),
  extractStream: `
var raw = pm.response.text();
var lines = raw.split("\\n");
var usageRaw = null, content = "", toolCalls = {};
for (var i = 0; i < lines.length; i++) {
  var line = lines[i];
  if (line.indexOf("data: ") !== 0) continue;
  var payload = line.slice(6).trim();
  if (payload === "[DONE]" || payload === "") continue;
  var chunk;
  try { chunk = JSON.parse(payload); } catch (e) { continue; }
  if (chunk.usage) usageRaw = chunk.usage;
  var choice = chunk.choices && chunk.choices[0];
  var delta = choice && choice.delta;
  if (delta && delta.content) content += delta.content;
  if (delta && delta.tool_calls) {
    delta.tool_calls.forEach(function (tc) {
      var idx = tc.index || 0;
      if (!toolCalls[idx]) toolCalls[idx] = { id: "", type: "function", function: { name: "", arguments: "" } };
      if (tc.id) toolCalls[idx].id = tc.id;
      if (tc.function && tc.function.name) toolCalls[idx].function.name += tc.function.name;
      if (tc.function && tc.function.arguments) toolCalls[idx].function.arguments += tc.function.arguments;
    });
  }
}
var u = usageRaw || {};
var usage = {
  prompt: u.prompt_tokens || 0,
  completion: u.completion_tokens || 0,
  cached: (u.prompt_tokens_details && u.prompt_tokens_details.cached_tokens) || 0,
  total: u.total_tokens || 0,
};
var toolCallList = Object.keys(toolCalls).map(function (k) { return toolCalls[k]; });
var assistantTurn = { role: "assistant" };
if (content) assistantTurn.content = content;
if (toolCallList.length) assistantTurn.tool_calls = toolCallList;
var toolCall = toolCallList[0] ? { id: toolCallList[0].id, name: toolCallList[0].function.name } : null;`.trim(),
};

// ---------------------------------------------------------------------------------------------
// Anthropic native Messages API shape (direct leg only)
// ---------------------------------------------------------------------------------------------
const anthropicShape = {
  turnsField: "messages",
  plainTurn: (text) => ({ role: "user", content: text }),
  mediaTurn: (kind, text) => {
    const block =
      kind === "image"
        ? { type: "image", source: { type: "base64", media_type: "image/jpeg", data: IMAGE_JPEG_BASE64 } }
        : { type: "document", source: { type: "base64", media_type: "application/pdf", data: DOCUMENT_PDF_BASE64 } };
    return { role: "user", content: [{ type: "text", text }, block] };
  },
  toolExtraParams: {
    tools: [
      {
        name: "get_weather",
        description: "Get the current weather for a city",
        input_schema: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
      },
    ],
    tool_choice: { type: "auto" },
  },
  toolResultTurn: (toolCallExpr) =>
    `{role:"user", content:[{type:"tool_result", tool_use_id: ${toolCallExpr}.id, content: ${J(TOOL_RESULT)}}]}`,
  extractNonStream: `
var j = pm.response.json();
var u = j.usage || {};
var usage = {
  prompt: u.input_tokens || 0,
  completion: u.output_tokens || 0,
  cached: u.cache_read_input_tokens || 0,
  total: (u.input_tokens || 0) + (u.output_tokens || 0),
};
var assistantTurn = { role: "assistant", content: j.content || [] };
var toolUse = (j.content || []).filter(function (b) { return b.type === "tool_use"; })[0];
var toolCall = toolUse ? { id: toolUse.id, name: toolUse.name } : null;`.trim(),
  extractStream: `
var raw = pm.response.text();
var lines = raw.split("\\n");
var inputTokens = 0, cacheRead = 0, outputTokens = 0;
var blocks = {};
for (var i = 0; i < lines.length; i++) {
  var line = lines[i];
  if (line.indexOf("data: ") !== 0) continue;
  var payload = line.slice(6).trim();
  if (!payload) continue;
  var evt;
  try { evt = JSON.parse(payload); } catch (e) { continue; }
  if (evt.type === "message_start" && evt.message && evt.message.usage) {
    inputTokens = evt.message.usage.input_tokens || 0;
    cacheRead = evt.message.usage.cache_read_input_tokens || 0;
  } else if (evt.type === "content_block_start") {
    var idx = evt.index;
    if (evt.content_block && evt.content_block.type === "tool_use") {
      blocks[idx] = { type: "tool_use", id: evt.content_block.id, name: evt.content_block.name, partial: "" };
    } else {
      blocks[idx] = { type: "text", text: "" };
    }
  } else if (evt.type === "content_block_delta") {
    var b = blocks[evt.index];
    if (!b) continue;
    if (evt.delta.type === "text_delta") b.text = (b.text || "") + evt.delta.text;
    else if (evt.delta.type === "input_json_delta") b.partial = (b.partial || "") + evt.delta.partial_json;
  } else if (evt.type === "message_delta") {
    if (evt.usage && typeof evt.usage.output_tokens === "number") outputTokens = evt.usage.output_tokens;
    if (evt.usage && typeof evt.usage.cache_read_input_tokens === "number") cacheRead = evt.usage.cache_read_input_tokens;
  }
}
var content = Object.keys(blocks).sort().map(function (k) {
  var b = blocks[k];
  if (b.type === "text") return { type: "text", text: b.text };
  var input = {};
  try { input = JSON.parse(b.partial || "{}"); } catch (e) {}
  return { type: "tool_use", id: b.id, name: b.name, input: input };
});
var usage = { prompt: inputTokens, completion: outputTokens, cached: cacheRead, total: inputTokens + outputTokens };
var assistantTurn = { role: "assistant", content: content };
var toolUse = content.filter(function (b) { return b.type === "tool_use"; })[0];
var toolCall = toolUse ? { id: toolUse.id, name: toolUse.name } : null;`.trim(),
};

// ---------------------------------------------------------------------------------------------
// Gemini generateContent shape - shared by AI Studio ("gemini") and Vertex AI ("vertex") direct
// legs; only the URL/auth differ, the wire shape and usageMetadata field names are identical.
// ---------------------------------------------------------------------------------------------
const geminiShape = {
  turnsField: "contents",
  plainTurn: (text) => ({ role: "user", parts: [{ text }] }),
  mediaTurn: (kind, text) => {
    const mimeType =
      kind === "image" ? "image/jpeg" : kind === "document" ? "application/pdf" : kind === "audio" ? "audio/wav" : "video/mp4";
    const data = kind === "image" ? IMAGE_JPEG_BASE64 : kind === "document" ? DOCUMENT_PDF_BASE64 : kind === "audio" ? AUDIO_WAV_BASE64 : VIDEO_MP4_BASE64;
    return { role: "user", parts: [{ text }, { inline_data: { mime_type: mimeType, data } }] };
  },
  toolExtraParams: {
    tools: [
      {
        functionDeclarations: [
          {
            name: "get_weather",
            description: "Get the current weather for a city",
            parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
          },
        ],
      },
    ],
  },
  toolResultTurn: (toolCallExpr) =>
    `{role:"user", parts:[{functionResponse:{name: ${toolCallExpr}.name, response:{content: ${J(TOOL_RESULT)}}}}]}`,
  extractNonStream: `
var j = pm.response.json();
var cand = (j.candidates && j.candidates[0]) || {};
var parts = (cand.content && cand.content.parts) || [];
var um = j.usageMetadata || {};
// Bifrost's own Gemini usage mapping (core/providers/gemini/utils.go) adds thoughtsTokenCount
// into CompletionTokens - Gemini 2.5 models think by default, and thinking tokens are billed
// output but reported in a separate usageMetadata field, not folded into candidatesTokenCount.
// Match that here or completion diverges wildly (candidatesTokenCount alone can be ~10-30
// while the real completion cost - candidates + thoughts - matches Bifrost's number).
var usage = {
  prompt: um.promptTokenCount || 0,
  completion: (um.candidatesTokenCount || 0) + (um.thoughtsTokenCount || 0),
  cached: um.cachedContentTokenCount || 0,
  total: um.totalTokenCount || 0,
};
var assistantTurn = { role: "model", parts: parts };
var fc = parts.filter(function (p) { return p.functionCall; })[0];
var toolCall = fc ? { name: fc.functionCall.name } : null;`.trim(),
  extractStream: `
var raw = pm.response.text();
var lines = raw.split("\\n");
var promptCount = 0, candCount = 0, thoughtsCount = 0, cachedCount = 0, totalCount = 0;
var textContent = "";
var fc = null;
for (var i = 0; i < lines.length; i++) {
  var line = lines[i];
  if (line.indexOf("data: ") !== 0) continue;
  var payload = line.slice(6).trim();
  if (!payload) continue;
  var chunk;
  try { chunk = JSON.parse(payload); } catch (e) { continue; }
  if (chunk.usageMetadata) {
    promptCount = chunk.usageMetadata.promptTokenCount || promptCount;
    candCount = chunk.usageMetadata.candidatesTokenCount || candCount;
    thoughtsCount = chunk.usageMetadata.thoughtsTokenCount || thoughtsCount;
    cachedCount = chunk.usageMetadata.cachedContentTokenCount || cachedCount;
    totalCount = chunk.usageMetadata.totalTokenCount || totalCount;
  }
  var cand = chunk.candidates && chunk.candidates[0];
  var parts = cand && cand.content && cand.content.parts;
  if (parts) {
    parts.forEach(function (p) {
      if (p.text) textContent += p.text;
      if (p.functionCall) fc = p.functionCall;
    });
  }
}
// See extractNonStream above: Bifrost folds thoughtsTokenCount into CompletionTokens.
var usage = { prompt: promptCount, completion: candCount + thoughtsCount, cached: cachedCount, total: totalCount };
var contentParts = fc ? [{ functionCall: fc }] : [{ text: textContent }];
var assistantTurn = { role: "model", parts: contentParts };
var toolCall = fc ? { name: fc.name } : null;`.trim(),
};

// ---------------------------------------------------------------------------------------------
// Bedrock Converse API shape (Claude on Bedrock, direct leg only). Streaming uses AWS's binary
// application/vnd.amazon.eventstream framing, not SSE - decodeEventStream() below hand-parses
// the frame format (length-prefixed headers + payload + CRC, CRC unchecked - this is a test
// harness, not a production consumer) since Postman has no AWS-SDK eventstream decoder built in.
// ---------------------------------------------------------------------------------------------
const EVENTSTREAM_DECODER = `
function decodeEventStream(buf) {
  var events = [];
  var offset = 0;
  while (offset + 12 <= buf.length) {
    var totalLen = buf.readUInt32BE(offset);
    if (totalLen <= 0 || offset + totalLen > buf.length) break;
    var headersLen = buf.readUInt32BE(offset + 4);
    var headersStart = offset + 12;
    var headersEnd = headersStart + headersLen;
    var payloadEnd = offset + totalLen - 4;
    var headers = {};
    var hOff = headersStart;
    while (hOff < headersEnd) {
      var nameLen = buf.readUInt8(hOff); hOff += 1;
      var name = buf.toString("utf8", hOff, hOff + nameLen); hOff += nameLen;
      var type = buf.readUInt8(hOff); hOff += 1;
      if (type === 7) {
        var valLen = buf.readUInt16BE(hOff); hOff += 2;
        headers[name] = buf.toString("utf8", hOff, hOff + valLen); hOff += valLen;
      } else if (type === 4) {
        headers[name] = buf.readInt32BE(hOff); hOff += 4;
      } else {
        break;
      }
    }
    events.push({ headers: headers, payload: buf.toString("utf8", headersEnd, payloadEnd) });
    offset += totalLen;
  }
  return events;
}`.trim();

const bedrockShape = {
  turnsField: "messages",
  plainTurn: (text) => ({ role: "user", content: [{ text }] }),
  mediaTurn: (kind, text) => {
    const block =
      kind === "image"
        ? { image: { format: "jpeg", source: { bytes: IMAGE_JPEG_BASE64 } } }
        : { document: { format: "pdf", name: "parity-sample", source: { bytes: DOCUMENT_PDF_BASE64 } } };
    return { role: "user", content: [{ text }, block] };
  },
  toolExtraParams: {
    toolConfig: {
      tools: [
        {
          toolSpec: {
            name: "get_weather",
            description: "Get the current weather for a city",
            inputSchema: { json: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } },
          },
        },
      ],
      toolChoice: { auto: {} },
    },
  },
  toolResultTurn: (toolCallExpr) =>
    `{role:"user", content:[{toolResult:{toolUseId: ${toolCallExpr}.id, content:[{text: ${J(TOOL_RESULT)}}], status:"success"}}]}`,
  extractNonStream: `
var j = pm.response.json();
var msg = (j.output && j.output.message) || {};
var content = msg.content || [];
var u = j.usage || {};
var usage = {
  prompt: u.inputTokens || 0,
  completion: u.outputTokens || 0,
  cached: u.cacheReadInputTokens || 0,
  total: u.totalTokens || ((u.inputTokens || 0) + (u.outputTokens || 0)),
};
var assistantTurn = { role: "assistant", content: content };
var toolUse = content.filter(function (b) { return b.toolUse; })[0];
var toolCall = toolUse ? { id: toolUse.toolUse.toolUseId, name: toolUse.toolUse.name } : null;`.trim(),
  extractStream: `
${EVENTSTREAM_DECODER}
var buf = Buffer.isBuffer(pm.response.stream) ? pm.response.stream : Buffer.from(pm.response.stream || []);
var events = decodeEventStream(buf);
var inputTokens = 0, outputTokens = 0, cacheRead = 0, totalTokens = 0;
var blocks = {};
for (var i = 0; i < events.length; i++) {
  var evt = events[i];
  var type = evt.headers[":event-type"];
  var data;
  try { data = JSON.parse(evt.payload); } catch (e) { continue; }
  if (type === "contentBlockStart") {
    var idx = data.contentBlockIndex;
    if (data.start && data.start.toolUse) {
      blocks[idx] = { type: "toolUse", id: data.start.toolUse.toolUseId, name: data.start.toolUse.name, partial: "" };
    } else {
      blocks[idx] = { type: "text", text: "" };
    }
  } else if (type === "contentBlockDelta") {
    // Real Bedrock Converse streaming responses omit contentBlockStart entirely for plain-text
    // blocks (confirmed via a live AWS SDK for Python capture: messageStart -> contentBlockDelta
    // -> ... with no contentBlockStart in between) - only tool_use blocks reliably get one (the client needs
    // the tool name/id upfront). Requiring contentBlockStart to lazily-init blocks[idx] silently
    // dropped every text delta whose block never got a start event, producing an empty assistant
    // turn that Bedrock's Converse API then rejected on the next round with "content field ...
    // is empty." Lazily default to a text block here too instead of skipping unseen indices.
    var b = blocks[data.contentBlockIndex];
    if (!b) { b = blocks[data.contentBlockIndex] = { type: "text", text: "" }; }
    if (data.delta && typeof data.delta.text === "string") b.text = (b.text || "") + data.delta.text;
    if (data.delta && data.delta.toolUse && typeof data.delta.toolUse.input === "string") b.partial = (b.partial || "") + data.delta.toolUse.input;
  } else if (type === "metadata") {
    if (data.usage) {
      inputTokens = data.usage.inputTokens || 0;
      outputTokens = data.usage.outputTokens || 0;
      cacheRead = data.usage.cacheReadInputTokens || 0;
      totalTokens = data.usage.totalTokens || (inputTokens + outputTokens);
    }
  }
}
var content = Object.keys(blocks).sort().map(function (k) {
  var b = blocks[k];
  if (b.type === "text") return { text: b.text };
  var input = {};
  try { input = JSON.parse(b.partial || "{}"); } catch (e) {}
  return { toolUse: { toolUseId: b.id, name: b.name, input: input } };
});
var usage = { prompt: inputTokens, completion: outputTokens, cached: cacheRead, total: totalTokens };
var assistantTurn = { role: "assistant", content: content };
var toolUseBlock = content.filter(function (b) { return b.toolUse; })[0];
var toolCall = toolUseBlock ? { id: toolUseBlock.toolUse.toolUseId, name: toolUseBlock.toolUse.name } : null;`.trim(),
};

// ---------------------------------------------------------------------------------------------
// Generic per-leg item builder: given a shape + connection info, produces the 3 round items.
// Round-2/3 bodies are pure `{{var}}` templates; each round's test script pre-computes the NEXT
// round's full turns array (see file header) and, on the final bifrost round, runs the parity
// assertions and writes the report blob consumed by analyze-token-parity.mjs.
// ---------------------------------------------------------------------------------------------
function buildLegItems({ leg, shape, backendKey, backendLabel, modality, urlFor, headerFor, authFor, model, modelField, extraParams, modelExpr }) {
  // modelExpr: a raw JS expression (NOT JSON.stringify'd) that evaluates at runtime to the
  // model actually hit - a literal for backends where the model is a static string in this
  // generator, or a pm.variables.get(...) read for backends where it's a Postman variable
  // (bedrock's {{bedrockModel}}) so the report shows the real resolved value, not the template.
  const resolvedModelExpr = modelExpr || J(model || "");
  const varPrefix = `pt_${backendKey}_${modality.key}_${leg}`;
  // The cache salt must stay out of varPrefix. "direct" and "bifrost" are different
  // strings of different lengths, so interpolating the leg name into the prompt made
  // the two legs of every row cost different token counts -- the one thing a parity
  // matrix must never do. It put a constant per-backend bias into the Δ column (anthropic +1,
  // openai/bedrock +2, gemini +2..+3 on every cell), far under the 20% tolerance but
  // large enough to hide a genuine few-token regression underneath the artifact.
  //
  // LEG_SALT keeps each leg its own cold cache (see buildQ) while costing both legs the
  // same token count: the ids differ only in a single trailing digit, and every
  // tokenizer in this matrix emits a lone digit as its own token. varPrefix keeps the
  // readable leg name, because it only ever names Postman variables and report rows,
  // which are not sent to a model.
  const promptSalt = `pt_${backendKey}_${modality.key}_${LEG_SALT[leg]}`;
  const isTools = modality.tools;
  const isFinalLegRound = (round) => round === 3;
  // Gives this leg its own cold cache in round 1 - see buildQ's comment for why an unsalted
  // shared LARGE_CONTEXT prefix makes the direct-vs-bifrost cached-token comparison a race.
  const Q = buildQ(promptSalt);

  const round1Turn = isTools
    ? shape.plainTurn(Q.tool1)
    : modality.media
    ? shape.mediaTurn(modality.media, Q.media1(MEDIA_WORD[modality.media]))
    : shape.plainTurn(Q.text1);

  const bodyFor = (turnsArrayLiteralOrVar, round) => {
    const body = {};
    if (modelField && model) body[modelField] = model;
    body[shape.turnsField] = turnsArrayLiteralOrVar;
    Object.assign(body, extraParams(round));
    return body;
  };

  const round1Body = bodyFor([round1Turn], 1);

  const items = [];

  // ---- Round 1 ----
  {
    const extract = modality.stream ? shape.extractStream : shape.extractNonStream;
    // Resolved entirely at generation time (not as a live ternary in the output) so the
    // generated script only ever contains ONE well-typed expression here - mixing a raw
    // JS-code string (tool result, references the runtime `toolCall` var) with a JSON-literal
    // object in the same interpolated slot would emit invalid syntax in whichever branch
    // wasn't taken, and a syntax error anywhere fails the whole script even if that branch
    // is provably dead code.
    const nextTurnsExpr = isTools
      ? `(toolCall ? sentTurns.concat([assistantTurn, ${shape.toolResultTurn("toolCall")}]) : sentTurns.concat([assistantTurn, ${J(shape.plainTurn(Q.tool3))}]))`
      : `sentTurns.concat([assistantTurn, ${J(shape.plainTurn(modality.media ? Q.media2 : Q.text2))}])`;
    const script = `
${extract}
pm.test(${J(`${backendLabel} ${modality.label} (${leg}) round 1: usage present`)}, function () {
  pm.expect(pm.response.code, "request failed: " + pm.response.text()).to.be.below(400);
  pm.expect(usage.total, "usage=" + JSON.stringify(usage)).to.be.a("number").that.is.above(0);
});
if (pm.response.code < 400) {
  var sentTurns = ${J([round1Turn])};
  var nextTurns = ${nextTurnsExpr};
  pm.collectionVariables.set(${J(varPrefix + "_r2body")}, JSON.stringify(nextTurns));
  // Round 1 is the only genuinely apples-to-apples prompt/cached comparison: identical fixed
  // input, nothing accumulated yet. Captured separately from the round-3 "_final" snapshot
  // (which the completion/total check still uses) so the verdict can compare each field against
  // the round where that field is actually clean, instead of everything against round 3's
  // conversation - which by then contains each leg's own independently-sampled prior replies.
  pm.collectionVariables.set(${J(varPrefix + "_r1")}, JSON.stringify(usage));
}`.trim();
    items.push({
      name: `Token parity: ${backendKey}/${modality.key} - ${leg} r1`,
      event: [{ listen: "test", script: { type: "text/javascript", exec: execLines(script) } }],
      request: {
        method: "POST",
        header: headerFor(modality.stream),
        ...(authFor ? { auth: authFor() } : {}),
        body: { mode: "raw", raw: JSON.stringify(round1Body, null, 2) },
        url: urlParts(urlFor(modality.stream)),
      },
    });
  }

  // ---- Round 2 ----
  {
    const extract = modality.stream ? shape.extractStream : shape.extractNonStream;
    const followUp3 = shape.plainTurn(modality.media ? Q.media3 : Q.text3);
    const script = `
${extract}
pm.test(${J(`${backendLabel} ${modality.label} (${leg}) round 2: usage present`)}, function () {
  pm.expect(pm.response.code, "request failed: " + pm.response.text()).to.be.below(400);
  pm.expect(usage.total, "usage=" + JSON.stringify(usage)).to.be.a("number").that.is.above(0);
});
if (pm.response.code < 400) {
  var sentTurns = JSON.parse(pm.collectionVariables.get(${J(varPrefix + "_r2body")}) || "[]");
  var nextTurns = sentTurns.concat([assistantTurn, ${J(followUp3)}]);
  pm.collectionVariables.set(${J(varPrefix + "_r3body")}, JSON.stringify(nextTurns));
}`.trim();
    const round2Body = bodyFor(`{{${varPrefix}_r2body}}`, 2);
    const raw = JSON.stringify(round2Body, null, 2).replace(
      new RegExp(`"\\{\\{${varPrefix}_r2body\\}\\}"`),
      `{{${varPrefix}_r2body}}`
    );
    items.push({
      name: `Token parity: ${backendKey}/${modality.key} - ${leg} r2`,
      event: [{ listen: "test", script: { type: "text/javascript", exec: execLines(script) } }],
      request: {
        method: "POST",
        header: headerFor(modality.stream),
        ...(authFor ? { auth: authFor() } : {}),
        body: { mode: "raw", raw },
        url: urlParts(urlFor(modality.stream)),
      },
    });
  }

  // ---- Round 3 ----
  {
    const extract = modality.stream ? shape.extractStream : shape.extractNonStream;
    const finalStore =
      leg === "direct"
        ? `
${resolveVars}
pm.collectionVariables.set(${J(`pt_${backendKey}_${modality.key}_direct_final`)}, JSON.stringify(Object.assign({endpoint: ptResolveVars(${J(urlFor(modality.stream))}), model: (${resolvedModelExpr})}, usage)));`.trim()
        : `
${within}
${resolveVars}
var directRaw = pm.collectionVariables.get(${J(`pt_${backendKey}_${modality.key}_direct_final`)});
var directR1Raw = pm.collectionVariables.get(${J(`pt_${backendKey}_${modality.key}_direct_r1`)});
var bifrostR1Raw = pm.collectionVariables.get(${J(`pt_${backendKey}_${modality.key}_bifrost_r1`)});
if (directRaw && directR1Raw && bifrostR1Raw) {
  var directFinal = JSON.parse(directRaw);
  var directR1 = JSON.parse(directR1Raw);
  var bifrostR1 = JSON.parse(bifrostR1Raw);
  // Reported/verdict "direct"/"bifrost" mix rounds deliberately: prompt/cached come from round 1
  // (the only genuinely clean comparison - identical fixed input, nothing accumulated), while
  // completion/total come from round 3 (the full 3-round conversation, which is the point of
  // running multiple rounds at all). See the round-1 capture comment above for why prompt/cached
  // specifically can't use round 3's numbers without inheriting noise neither leg controls.
  var direct = { prompt: directR1.prompt, cached: directR1.cached, completion: directFinal.completion, total: directFinal.total, endpoint: directFinal.endpoint, model: directFinal.model };
  var bifrostReport = { prompt: bifrostR1.prompt, cached: bifrostR1.cached, completion: usage.completion, total: usage.total, endpoint: ptResolveVars(${J(urlFor(modality.stream))}), model: (${resolvedModelExpr}), baseUrl: pm.variables.get("baseUrl") };
  // reasoning_on/reasoning_on_streaming: a thinkingBudget is a CAP on how much a model CAN
  // think, not how much it DOES think on a given call - that decision is genuinely stochastic
  // per call even with an identical cap on both legs (confirmed against real run data: up to
  // ~44% completion swings with no truncation involved). No tolerance band makes that a
  // meaningful pass/fail signal, so these two modalities are reported (numbers still captured
  // and shown) but not asserted - the goal here is "does the reasoning-enabled wire path work
  // at all", not "are completion counts numerically close", which isn't achievable here.
  var isReasoningOn = ${J(modality.reasoning === "on")};
  if (!isReasoningOn) {
    pm.test(${J(`Token parity ${backendKey}/${modality.key}: prompt tokens within ${TOLERANCE_PCT}%`)}, function () {
      pm.expect(ptWithinPct(direct.prompt, bifrostReport.prompt, ${TOLERANCE_PCT}), "direct=" + direct.prompt + " bifrost=" + bifrostReport.prompt).to.equal(true);
    });
    // cached is REPORTED, not asserted, and cannot be otherwise in this suite. No body here sets
    // a cache_control breakpoint (see the Claude note at the top of this file), so a non-zero
    // cached count can only come from automatic/implicit caching - and that is unusable as a
    // parity signal for two independent reasons:
    //
    //   1. Implicit caching keys off an exact byte prefix, and the two legs deliberately send
    //      different bytes. The direct leg posts each backend's native shape while Bifrost
    //      normalizes every backend onto one payload, so the legs can never share an implicit
    //      cache entry. Measured: vertex/tools reported direct cached=5764 against bifrost
    //      cached=0 while the two prompt counts were within 28 tokens - the miss was the
    //      differing bytes, not a gateway defect.
    //   2. Implicit caching is best-effort even leg-to-leg. Google promises only to pass on
    //      savings "if your request hits caches" (https://ai.google.dev/gemini-api/docs/caching),
    //      and byte-identical repeats measured here alternate between a miss and a full hit.
    //
    // Assert what stays invariant regardless of whether a cache engaged - the counter is a real
    // number and can never exceed the prompt it came from - so a nonsense value is still caught,
    // and keep both numbers in the TOKEN_PARITY_REPORT below so the matrix still shows them.
    pm.test(${J(`Token parity ${backendKey}/${modality.key}: cached tokens coherent (implicit cache, cross-leg parity not achievable)`)}, function () {
      pm.expect(bifrostReport.cached, "bifrost cached=" + bifrostReport.cached).to.be.a("number").that.is.at.least(0);
      pm.expect(bifrostReport.cached, "bifrost cached=" + bifrostReport.cached + " exceeds prompt=" + bifrostReport.prompt).to.be.at.most(bifrostReport.prompt);
    });
    // Hybrid tolerance on completion: independent short free-text answers naturally differ by a
    // handful of tokens regardless of round (see the completion-noise category in the report
    // analysis) - at small absolute magnitudes that's a large percentage but a trivial real
    // difference, so pass on EITHER a relative OR a small absolute match, whichever is looser.
    // A genuinely large swing (both far outside the % band AND outside the absolute floor)
    // still fails - this doesn't weaken detection of real drift, it stops small numbers from
    // manufacturing false positives out of ordinary sampling noise.
    pm.test(${J(`Token parity ${backendKey}/${modality.key}: completion tokens within ${TOLERANCE_PCT}% or ${COMPLETION_ABS_FLOOR} tokens`)}, function () {
      var ok = ptWithinPct(direct.completion, bifrostReport.completion, ${TOLERANCE_PCT}) || Math.abs(direct.completion - bifrostReport.completion) <= ${COMPLETION_ABS_FLOOR};
      pm.expect(ok, "direct=" + direct.completion + " bifrost=" + bifrostReport.completion).to.equal(true);
    });
  }
  console.log("TOKEN_PARITY_REPORT", JSON.stringify({
    backend: ${J(backendKey)}, modality: ${J(modality.key)}, label: ${J(modality.label)}, skippedFromVerdict: isReasoningOn,
    direct: direct,
    bifrost: bifrostReport,
  }));
}`.trim();
    const script = `
${extract}
pm.test(${J(`${backendLabel} ${modality.label} (${leg}) round 3: usage present`)}, function () {
  pm.expect(pm.response.code, "request failed: " + pm.response.text()).to.be.below(400);
  pm.expect(usage.total, "usage=" + JSON.stringify(usage)).to.be.a("number").that.is.above(0);
});
if (pm.response.code < 400) {
${finalStore}
}`.trim();
    const round3Body = bodyFor(`{{${varPrefix}_r3body}}`, 3);
    const raw = JSON.stringify(round3Body, null, 2).replace(
      new RegExp(`"\\{\\{${varPrefix}_r3body\\}\\}"`),
      `{{${varPrefix}_r3body}}`
    );
    items.push({
      name: `Token parity: ${backendKey}/${modality.key} - ${leg} r3`,
      event: [{ listen: "test", script: { type: "text/javascript", exec: execLines(script) } }],
      request: {
        method: "POST",
        header: headerFor(modality.stream),
        ...(authFor ? { auth: authFor() } : {}),
        body: { mode: "raw", raw },
        url: urlParts(urlFor(modality.stream)),
      },
    });
  }

  return items;
}

// ---------------------------------------------------------------------------------------------
// Per-backend cell builders
// ---------------------------------------------------------------------------------------------
// modality.reasoning ("off" | "on" | undefined): Gemini 2.5 models think by default with a
// DYNAMIC budget - with only ~300 output tokens available, thinking alone can consume the whole
// budget before any visible answer is produced, and thinking length is independently non-
// deterministic on each leg. That produces exactly the symptom seen in real runs: completion
// counts clustering right under the cap (e.g. 296) and wild, direction-flipping direct-vs-
// bifrost deltas that have nothing to do with real accounting drift.
//   "off" forces thinkingBudget=0 (native Gemini thinkingConfig) so the other 8 modality
//     classes measure real answer tokens only.
//   "on" forces an EXPLICIT fixed budget (REASONING_ON_BUDGET, not dynamic/auto), with a
//     larger max_tokens so thinking + the actual answer both fit - a fixed budget keeps the
//     comparison apples-to-apples instead of racing two independently-unpredictable thinking
//     traces. Scoped to gemini/vertex only (see reasoning_on/reasoning_on_streaming SKIP
//     entries) since no other backend in this matrix uses a reasoning-capable model.
//
// Same logic for both legs: the bifrost leg now hits each backend's OWN drop-in route
// (/openai, /anthropic, /genai, /bedrock) in that provider's native wire shape, not the
// OpenAI-normalized unified endpoint, so there's no separate "isBifrost" translation needed
// anymore - whatever native reasoning/tool/max-tokens fields the direct leg uses are exactly
// what the matching drop-in route expects too.
function extraParamsFor(shape, modality) {
  const reasoning = modality.reasoning;
  return () => {
    const p = {};
    if (shape === openaiShape) {
      p.max_tokens = 300;
      if (modality.stream) {
        p.stream = true;
        p.stream_options = { include_usage: true };
      }
      if (modality.tools) Object.assign(p, shape.toolExtraParams);
    } else if (shape === anthropicShape) {
      p.max_tokens = 300;
      if (modality.stream) p.stream = true;
      if (modality.tools) Object.assign(p, shape.toolExtraParams);
    } else if (shape === geminiShape) {
      p.generationConfig = { maxOutputTokens: reasoning === "on" ? 300 + REASONING_ON_BUDGET : 300 };
      if (reasoning === "off") p.generationConfig.thinkingConfig = { thinkingBudget: 0 };
      else if (reasoning === "on") p.generationConfig.thinkingConfig = { thinkingBudget: REASONING_ON_BUDGET };
      if (modality.tools) Object.assign(p, shape.toolExtraParams);
    } else if (shape === bedrockShape) {
      p.inferenceConfig = { maxTokens: 300 };
      if (modality.tools) Object.assign(p, shape.toolExtraParams);
    }
    return p;
  };
}

function buildOpenaiDirect(modality) {
  const model = modality.key === "audio" ? "gpt-audio-1.5" : "gpt-4o-mini";
  return buildLegItems({
    leg: "direct",
    shape: openaiShape,
    backendKey: "openai",
    backendLabel: "OpenAI",
    modality,
    urlFor: () => "https://api.openai.com/v1/chat/completions",
    headerFor: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "Authorization", value: "Bearer {{openaiKey}}" },
    ],
    model,
    modelField: "model",
    modelExpr: J(model),
    extraParams: extraParamsFor(openaiShape, modality),
  });
}

function buildAnthropicDirect(modality) {
  return buildLegItems({
    leg: "direct",
    shape: anthropicShape,
    backendKey: "anthropic",
    backendLabel: "Anthropic",
    modality,
    urlFor: () => "https://api.anthropic.com/v1/messages",
    headerFor: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "x-api-key", value: "{{anthropicKey}}" },
      { key: "anthropic-version", value: "2023-06-01" },
    ],
    model: "claude-haiku-4-5",
    modelField: "model",
    modelExpr: J("claude-haiku-4-5"),
    extraParams: extraParamsFor(anthropicShape, modality),
  });
}

function buildGeminiFamilyDirect(backendKey, backendLabel, modality) {
  const isVertex = backendKey === "vertex";
  const urlFor = (stream) => {
    const method = stream ? "streamGenerateContent?alt=sse" : "generateContent";
    if (isVertex) {
      return `https://{{vertexLocation}}-aiplatform.googleapis.com/v1/projects/{{vertexProject}}/locations/{{vertexLocation}}/publishers/google/models/gemini-2.5-flash:${method}`;
    }
    return `https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:${method}`;
  };
  const headerFor = () =>
    isVertex
      ? [{ key: "Content-Type", value: "application/json" }, { key: "Authorization", value: "Bearer {{vertexAccessToken}}" }]
      : [{ key: "Content-Type", value: "application/json" }, { key: "x-goog-api-key", value: "{{genaiKey}}" }];
  return buildLegItems({
    leg: "direct",
    shape: geminiShape,
    backendKey,
    backendLabel,
    modality,
    urlFor,
    headerFor,
    model: null,
    modelField: null,
    modelExpr: J("gemini-2.5-flash"),
    extraParams: extraParamsFor(geminiShape, modality),
  });
}

// modelVar: the Postman variable holding the raw Bedrock modelId (account/region-specific -
// Bedrock model access is gated per-account, so this is always a variable, never a literal,
// matching how the rest of this harness already treats {{bedrockModel}}). Parameterized so the
// same builder covers both the existing Claude-on-Bedrock backend and the "one more model per
// provider" OpenAI-family (gpt-oss)-on-Bedrock addition below - Bedrock's Converse API is
// model-family-agnostic, so nothing else about the direct call changes.
function buildBedrockDirect(backendKey, backendLabel, modelVar, modality) {
  return buildLegItems({
    leg: "direct",
    shape: bedrockShape,
    backendKey,
    backendLabel,
    modality,
    urlFor: (stream) => `https://bedrock-runtime.{{bedrockDirectRegion}}.amazonaws.com/model/{{${modelVar}}}/converse${stream ? "-stream" : ""}`,
    headerFor: () => [{ key: "Content-Type", value: "application/json" }],
    authFor: () => ({
      type: "awsv4",
      awsv4: [
        { key: "accessKey", value: "{{bedrockDirectAccessKeyId}}", type: "string" },
        { key: "secretKey", value: "{{bedrockDirectSecretAccessKey}}", type: "string" },
        { key: "region", value: "{{bedrockDirectRegion}}", type: "string" },
        { key: "service", value: "bedrock", type: "string" },
      ],
    }),
    model: null,
    modelField: null,
    modelExpr: `pm.variables.get(${J(modelVar)})`,
    extraParams: extraParamsFor(bedrockShape, modality),
  });
}

// Claude models hosted on Vertex AI use Anthropic's own Messages API wire shape (reuses
// anthropicShape entirely - same turns/tools/extraction), just via a different endpoint, auth,
// and two body differences: `anthropic_version` goes in the body (there's no anthropic-version
// header route here) and `model` is omitted from the body since it's already in the URL path.
// Source: https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai (verified
// non-streaming :rawPredict path; the streaming endpoint is NOT documented there and wasn't
// independently confirmed, so streaming modalities are SKIPped for this backend rather than
// guessed - see SKIP matrix).
const VERTEX_CLAUDE_MODEL = "claude-sonnet-4-6"; // matches the existing Round 16 vertex/claude-sonnet-4-6 rows
function buildVertexClaudeDirect(modality) {
  return buildLegItems({
    leg: "direct",
    shape: anthropicShape,
    backendKey: "vertex_claude",
    backendLabel: "Vertex AI (Claude)",
    modality,
    // Claude models on Vertex are only servable via the "global" location, not a region
    // (confirmed both by the research doc and, more concretely, by Bifrost's OWN
    // tests/integrations/python/config.json, which blacklists claude-sonnet-4-6/claude-opus-4-8
    // from its regional vertex key entirely and uses a separate region:"global" key just for
    // those models). {{vertexLocation}} (e.g. us-central1) is right for Gemini-on-Vertex but
    // 400s for Claude - confirmed live: "Publisher Model ... is not servable in region
    // us-central1." The global endpoint also drops the region prefix from the host.
    urlFor: () =>
      `https://aiplatform.googleapis.com/v1/projects/{{vertexProject}}/locations/global/publishers/anthropic/models/${VERTEX_CLAUDE_MODEL}:rawPredict`,
    headerFor: () => [{ key: "Content-Type", value: "application/json" }, { key: "Authorization", value: "Bearer {{vertexAccessToken}}" }],
    model: null,
    modelField: null,
    modelExpr: J(VERTEX_CLAUDE_MODEL),
    extraParams: () => Object.assign(extraParamsFor(anthropicShape, modality)(), { anthropic_version: "vertex-2023-10-16" }),
  });
}

// ---------------------------------------------------------------------------------------------
// Per-backend BIFROST-leg builders. Each hits Bifrost's own drop-in route for that provider
// (/openai, /anthropic, /genai, /bedrock) using that provider's native wire shape - the exact
// same shape/turn/tool helpers as that backend's DIRECT-leg builder above - so Bifrost's own
// translation layer is exercised the same way a real SDK client for that provider would exercise
// it, rather than routing everything through the OpenAI-normalized unified /v1/chat/completions
// endpoint. Routing/auth per backend confirmed against Bifrost's own route registration (not
// guessed): openai.go CreateOpenAIRouteConfigs("/openai", ...) registers pathPrefix+"/v1/chat/
// completions"; anthropic.go CreateAnthropicRouteConfigs("/anthropic", ...) registers pathPrefix+
// "/v1/messages"; genai.go registers CreateGenAIRouteConfigs("/genai") with model+action folded
// into the URL path ("/v1beta/models/{model}:generateContent"); bedrock.go registers
// CreateBedrockRouteConfigs("/bedrock", ...) with pathPrefix+"/model/{modelId}/converse(-stream)".
// Auth headers per route confirmed against EXISTING checked-in harness items already hitting
// these same drop-in routes elsewhere in provider-harness.json (Anthropic computer-use items send
// x-api-key; Gemini/GenAI Backlog items send x-goog-api-key; native Bedrock Converse items send
// no auth header at all - Bifrost resolves AWS credentials server-side).
// ---------------------------------------------------------------------------------------------
function buildOpenaiBifrost(modality) {
  const model = modality.key === "audio" ? "gpt-audio-1.5" : "gpt-4o-mini";
  return buildLegItems({
    leg: "bifrost",
    shape: openaiShape,
    backendKey: "openai",
    backendLabel: "OpenAI",
    modality,
    urlFor: () => "{{baseUrl}}/openai/v1/chat/completions",
    headerFor: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "Authorization", value: "Bearer {{openaiKey}}" },
    ],
    model,
    modelField: "model",
    modelExpr: J(model),
    extraParams: extraParamsFor(openaiShape, modality),
  });
}

function buildAnthropicBifrost(modality) {
  return buildLegItems({
    leg: "bifrost",
    shape: anthropicShape,
    backendKey: "anthropic",
    backendLabel: "Anthropic",
    modality,
    urlFor: () => "{{baseUrl}}/anthropic/v1/messages",
    headerFor: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "x-api-key", value: "{{anthropicKey}}" },
      { key: "anthropic-version", value: "2023-06-01" },
    ],
    model: "claude-haiku-4-5",
    modelField: "model",
    modelExpr: J("claude-haiku-4-5"),
    extraParams: extraParamsFor(anthropicShape, modality),
  });
}

// Gemini (AI Studio) and Vertex AI (Gemini) share the exact same /genai URL when hitting Bifrost -
// unlike the direct leg, where Vertex needs its own aiplatform.googleapis.com host with a
// project/location path. Bifrost's shared drop-in route instead disambiguates Gemini vs Vertex
// from the Authorization header: a "Bearer ya29...." token (real GCP OAuth prefix) selects Vertex,
// anything else falls back to Gemini (detectProviderFromGenAIRequest in genai.go). Reuses the same
// gcloud-minted {{vertexAccessToken}} already forwarded for the direct Vertex leg.
function buildGeminiFamilyBifrost(backendKey, backendLabel, modality) {
  const isVertex = backendKey === "vertex";
  const urlFor = (stream) => {
    const method = stream ? "streamGenerateContent?alt=sse" : "generateContent";
    return `{{baseUrl}}/genai/v1beta/models/gemini-2.5-flash:${method}`;
  };
  const headerFor = () =>
    isVertex
      ? [{ key: "Content-Type", value: "application/json" }, { key: "Authorization", value: "Bearer {{vertexAccessToken}}" }]
      : [{ key: "Content-Type", value: "application/json" }, { key: "x-goog-api-key", value: "{{genaiKey}}" }];
  return buildLegItems({
    leg: "bifrost",
    shape: geminiShape,
    backendKey,
    backendLabel,
    modality,
    urlFor,
    headerFor,
    model: null,
    modelField: null,
    modelExpr: J("gemini-2.5-flash"),
    extraParams: extraParamsFor(geminiShape, modality),
  });
}

// Shared by both Bedrock backends (Claude and the "one more model per provider" OpenAI/gpt-oss
// family) - same modelVar parameterization as buildBedrockDirect. No auth header: Bifrost's
// /bedrock drop-in route resolves its own server-side AWS credentials, same as every other
// existing /bedrock item already in this harness.
function buildBedrockBifrost(backendKey, backendLabel, modelVar, modality) {
  return buildLegItems({
    leg: "bifrost",
    shape: bedrockShape,
    backendKey,
    backendLabel,
    modality,
    urlFor: (stream) => `{{baseUrl}}/bedrock/model/{{${modelVar}}}/converse${stream ? "-stream" : ""}`,
    headerFor: () => [{ key: "Content-Type", value: "application/json" }],
    model: null,
    modelField: null,
    modelExpr: `pm.variables.get(${J(modelVar)})`,
    extraParams: extraParamsFor(bedrockShape, modality),
  });
}

// Claude-on-Vertex via Bifrost hits the same /anthropic/v1/messages drop-in route as native
// Anthropic - Bifrost's schemas.ParseModelString() splits the provider-prefixed "vertex/..."
// model string and routes to the Vertex provider internally (confirmed in anthropic.go's
// checkAnthropicPassthrough: `provider, model = schemas.ParseModelString(r.Model, "")`). No
// vertexAccessToken/auth header needed - provider != Anthropic/"" so the OAuth-passthrough branch
// never engages, and Bifrost resolves its own server-side Vertex credentials instead, same as the
// old unified-endpoint bifrost leg for this backend. anthropic_version is a header-only concern on
// the real Anthropic API (not a body field) and Bifrost's own Vertex-Claude provider code sets
// whatever internal version string that upstream call needs - the vertex-2023-10-16 body field
// the DIRECT leg sends is a Vertex rawPredict-specific requirement, not part of this wire shape.
function buildVertexClaudeBifrost(modality) {
  const model = `vertex/${VERTEX_CLAUDE_MODEL}`;
  return buildLegItems({
    leg: "bifrost",
    shape: anthropicShape,
    backendKey: "vertex_claude",
    backendLabel: "Vertex AI (Claude)",
    modality,
    urlFor: () => "{{baseUrl}}/anthropic/v1/messages",
    headerFor: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "anthropic-version", value: "2023-06-01" },
    ],
    model,
    modelField: "model",
    modelExpr: J(model),
    extraParams: extraParamsFor(anthropicShape, modality),
  });
}

const BACKENDS = [
  { key: "openai", label: "OpenAI", direct: buildOpenaiDirect, bifrost: buildOpenaiBifrost },
  { key: "anthropic", label: "Anthropic", direct: buildAnthropicDirect, bifrost: buildAnthropicBifrost },
  {
    key: "gemini",
    label: "Gemini (AI Studio)",
    direct: (m) => buildGeminiFamilyDirect("gemini", "Gemini (AI Studio)", m),
    bifrost: (m) => buildGeminiFamilyBifrost("gemini", "Gemini (AI Studio)", m),
  },
  {
    key: "vertex",
    label: "Vertex AI (Gemini)",
    direct: (m) => buildGeminiFamilyDirect("vertex", "Vertex AI (Gemini)", m),
    bifrost: (m) => buildGeminiFamilyBifrost("vertex", "Vertex AI (Gemini)", m),
  },
  {
    key: "bedrock",
    label: "Bedrock (Claude)",
    direct: (m) => buildBedrockDirect("bedrock", "Bedrock (Claude)", "bedrockModel", m),
    bifrost: (m) => buildBedrockBifrost("bedrock", "Bedrock (Claude)", "bedrockModel", m),
  },
  // "One more model per provider": Bedrock and Vertex both host more than one model family.
  {
    key: "bedrock_openai",
    label: "Bedrock (OpenAI/gpt-oss)",
    direct: (m) => buildBedrockDirect("bedrock_openai", "Bedrock (OpenAI/gpt-oss)", "bedrockOpenaiModel", m),
    bifrost: (m) => buildBedrockBifrost("bedrock_openai", "Bedrock (OpenAI/gpt-oss)", "bedrockOpenaiModel", m),
  },
  { key: "vertex_claude", label: "Vertex AI (Claude)", direct: buildVertexClaudeDirect, bifrost: buildVertexClaudeBifrost },
];

export function buildTokenParityMatrix() {
  const items = [];
  const skipNotes = [];

  for (const backend of BACKENDS) {
    for (const modality of MODALITIES) {
      const skipReason = SKIP[backend.key] && SKIP[backend.key][modality.key];
      if (skipReason) {
        skipNotes.push(`${backend.key}/${modality.key}: ${skipReason}`);
        continue;
      }
      items.push(...backend.direct(modality));
      items.push(...backend.bifrost(modality));
    }
  }

  return {
    name: "Cross-Cut Round 33: Direct-Provider vs Bifrost Token Parity Matrix (generated)",
    description:
      "Generated at harness runtime. Runs the same fixed 3-round conversation against each provider's native API and against Bifrost's own drop-in integration route for that same provider (/openai, /anthropic, /genai, /bedrock) in that provider's native wire shape - not the OpenAI-normalized unified /v1/chat/completions endpoint - then asserts token usage lands in the same range " +
      `(prompt within ${TOLERANCE_PCT}% of round 1 - the only round with nothing accumulated from either leg's own prior replies; completion within ${TOLERANCE_PCT}% or ${COMPLETION_ABS_FLOOR} tokens of round 3's cumulative usage, whichever is looser; cached reported but not asserted). ` +
      "reasoning_on/reasoning_on_streaming are reported but not asserted - a thinkingBudget caps how much a model CAN think, not how much it DOES on a given call, so completion swings there reflect real per-call stochasticity, not drift. " +
      "Covers text, tool-calling, streaming, and image/document/audio/video input across openai/anthropic/gemini/vertex/bedrock, plus a second model family each for the two multi-model-family providers (bedrock_openai: gpt-oss-on-Bedrock; vertex_claude: Claude-on-Vertex), minus provider-capability SKIPs (see SKIP matrix in token-parity-matrix.mjs). " +
      "Also covers reasoning explicitly on (fixed budget, not dynamic) and off for gemini/vertex, since Gemini 2.5 thinks by default with a non-deterministic dynamic budget that otherwise dominates completion-token noise. " +
      `Skipped cells this run: ${skipNotes.join("; ") || "none"}. ` +
      "Report: tmp/harness-token-parity.md + injected into the interactive viewer at :8090 (via render-token-parity-report.mjs). Credentials reuse the same env vars tests/integrations/python/config.json already reads (OPENAI_API_KEY/ANTHROPIC_API_KEY/GEMINI_API_KEY/AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION/VERTEX_PROJECT_ID/GOOGLE_LOCATION), plus a gcloud-minted vertexAccessToken (see Makefile).",
    item: items,
  };
}
