#!/usr/bin/env python3
"""Writes test/cases/*.json: the eight worked examples of docs/examples.md as test cases.

Run `go generate ./test/cases` from the repo root after editing. Bodies use 4 bytes a token, and a body
hash is cumulative over the content ids, so two bodies with an equal prefix have equal hashes
there (design.md §5.1). Every figure is checked twice: cases_test.go checks conservation and the
billed tokens against the bodies. A host can also play each case through its own saving code and
compare (context-guru does, in dash/accountant_play_test.go).
"""
import json
import os
from datetime import datetime, timedelta, timezone

OUT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "cases")
T0 = datetime(2026, 3, 2, 9, 0, 0, tzinfo=timezone.utc)
MASK = (1 << 64) - 1

BSTAR_SNAP = ("design §5.1 snaps B* to the last client breakpoint at or before the previous body's end, "
              "and the client's breakpoints are the current request's own: system and the new last message. "
              "Read literally that gives the end of system, not the end of the previous body. The examples, and "
              "this case, take the end of the previous body.")


def ts(sec):
    return (T0 + timedelta(seconds=sec)).strftime("%Y-%m-%dT%H:%M:%SZ")


def body(tools, system, msgs):
    h, pos = 14695981039346656037, []
    for section in (tools, system, msgs):
        for cid, tok in section:
            for ch in (cid + "\0").encode():
                h = ((h ^ ch) * 1099511628211) & MASK
            pos.append({"hash": h, "bytes": 4 * tok})
    return {"start": [0, len(tools), len(tools) + len(system)], "pos": pos}


def tokens(*sections):
    return sum(t for s in sections for _, t in s)


def P(section, index):
    return {"section": section, "index": index}


def bps(msgs, ttl="5m", system=True):
    out = [{"pos": P("system", 0), "ttl": ttl}] if system else []
    out.append({"pos": P("messages", len(msgs) - 1), "ttl": ttl})
    return out


def U(read=0, write=0, inp=0, w1h=0):
    return {"input": inp, "cacheRead": read, "cacheWrite": write, "cacheWrite1h": w1h}


def TT(**kw):
    return kw  # input, cacheWrite5m, cacheWrite1h, cacheRead


def size(t):
    # Bytes only (design §4.2 rule 7). The fixtures use 4 bytes a token, so t tokens are 4 * t bytes.
    return {"bytes": 4 * t}


def rewrite(oid, operator, section, idx, before, after, change, spans=None, splice=None):
    op = {"id": oid, "kind": "rewrite", "operator": operator, "at": P(section, idx),
          "sizeBefore": size(before), "sizeAfter": size(after), "changeId": change}
    if spans:
        op["spans"] = spans
    if splice:
        op["messageSplice"] = splice
    return op


def eop(change, saved=None, caused=None, note=None, call=None):
    e = {"changeId": change, "saved": saved or {}, "caused": caused or {}}
    if not call:  # a rewrite's tokens are converted from bytes, so it is Estimated (§4.2 rule 7)
        e["estimated"] = True
    if note:
        e["note"] = note
    if call:
        e["call"] = call
    return e


def step(sec, rid, client, fwd, cbp, fbp, add, usage, exp, session="s1", model="agent-model",
         params=None, facts=None, thread=None, compaction=None, model_key=None):
    meta = {"requestId": rid, "session": session, "model": model, "at": ts(sec), "billing": {"mode": "api"}}
    if model_key:  # the host's name for the model apart from the spelling of its id (accountant#44)
        meta["modelKey"] = model_key
    if compaction:  # the host's verdict of a client compaction manual | auto | unknown
        meta["compaction"] = compaction
    if params:
        meta["params"] = params
    return {
        "begin": {"meta": meta, "client": client, "clientBreakpoints": cbp},
        "add": add,
        "end": {"thread": thread or {"id": "main"}, "forwarded": fwd, "breakpoints": fbp,
                "facts": facts or {"ttl": "5m", "lifetime": "exact"}},
        "usage": usage,
        "expected": exp,
    }


def effect(kind, change, saved=None, caused=None, frm=None, to=None, ttl=None, note=None, baseline=None, cause=None):
    """A cache effect the library finds (design 4.4). It has no input form: only Compute writes one."""
    e = {"kind": kind, "changeId": change, "saved": saved or {}, "caused": caused or {}}
    if baseline is None:  # a break nobody caused is in the client body's bill too, so it cancels out (§6.3)
        baseline = kind == "break" and change == ""
    if baseline:
        e["inBaseline"] = True
    if frm == "nil":  # asserts that From is nil: the range starts at the prompt
        e["fromNil"] = True
    elif frm:
        e["from"] = frm
    if to:
        e["to"] = to
    if ttl:
        e["ttl"] = ttl
    if cause:
        e["cause"] = cause
    if note:
        e["note"] = note
    return e


def expect(phase, baseline, ops, boundary=None, unpriced=None, effects=None, checks=None, ended=None, compaction=None):
    e = {"phase": phase, "baselineUsage": baseline, "ops": ops}
    if compaction:  # the record the library returns for the verdict, with the changes that ended
        e["compaction"] = {"trigger": compaction, "ended": ended or []}
    if phase == "cold" and not effects:  # §5.2 row 5: the expiry is on the record, and it moves no tokens
        effects = [effect("break", "", note="the entry expired; B* is 0, so the read extent to B* is empty")]
    if effects:
        e["effects"] = effects
    if boundary:
        e["boundary"] = boundary
    if unpriced:
        e["unpriced"] = unpriced
    if checks:
        e["checks"] = checks
    return e


def hot_usage(prev_total, total):
    return U(read=prev_total, write=total - prev_total)


def case(cid, description, source, dims, steps, rates="fixture-v1", open_=None):
    c = {"id": cid, "description": description, "source": source, "dims": dims, "rates": rates}
    if open_:
        c["open"] = open_
    c["steps"] = steps
    return c


def write(c, name):
    with open(os.path.join(OUT, name + ".json"), "w") as f:
        json.dump(c, f, indent=1)
        f.write("\n")


# ---------------------------------------------------------------------------------------------
# Examples 1 and 2: one rewrite of a new message, on hot and cold requests.
SYS1 = [("sys1", 2000)]
M_BASE = [("m0", 4000)] + [(f"m{i}", 3000) for i in range(1, 9)]  # 30000 with the system block
CF = "cg:cmdfilter:c40e"


def cmdfilter_op():
    return rewrite("o1", "context-guru.cmdfilter", "messages", 9, 8000, 1000, CF)


def ex12():
    steps = [step(0, "r0", body([], SYS1, M_BASE), body([], SYS1, M_BASE), bps(M_BASE), bps(M_BASE), [],
                  U(write=30000), expect("unknown", U(write=30000), []))]
    c_msgs = M_BASE + [("m9", 8000)]
    f_msgs = M_BASE + [("m9f", 1000)]
    steps.append(step(10, "r1", body([], SYS1, c_msgs), body([], SYS1, f_msgs), bps(c_msgs), bps(f_msgs),
                      [cmdfilter_op()], U(read=30000, write=1000),
                      expect("hot", U(read=30000, write=8000),
                             [eop(CF, saved=TT(cacheWrite5m=7000), note="new content, would have been written")],
                             boundary=P("messages", 8))))
    one = case("rewrite/single/after-bstar/hit/5m",
               "One hot request. A new 8,000-token test output (message 9) is shortened to 1,000 tokens by "
               "cmdfilter. The cache holds messages 0 to 8, so B* is the end of message 8 and the 7,000 removed "
               "tokens would have been cache writes. Saves 7,000 x $3.75/M = $0.02625.",
               "examples.md#1", {"kind": "rewrite", "position": "afterBStarToL", "cache": "hit", "ttl": "5m",
                                 "lifetime": "exact"}, steps, open_=[BSTAR_SNAP])
    write(one, "rewrite-single-after-bstar")

    c2, f2 = c_msgs + [("m10", 500)], f_msgs + [("m10", 500)]
    steps.append(step(70, "r2", body([], SYS1, c2), body([], SYS1, f2), bps(c2), bps(f2), [],
                      U(read=31000, write=500),
                      expect("hot", U(read=38000, write=500),
                             [eop(CF, saved=TT(cacheRead=7000), note="applied again; the client's prefix would be cached")],
                             boundary=P("messages", 9))))
    c3, f3 = c2 + [("m11", 500)], f2 + [("m11", 500)]
    steps.append(step(1270, "r3", body([], SYS1, c3), body([], SYS1, f3), bps(c3), bps(f3), [],
                      U(write=32000),
                      expect("cold", U(write=39000),
                             [eop(CF, saved=TT(cacheWrite5m=7000), note="the entry expired; written again")])))
    write(case("rewrite/replay/hot-hot-cold/5m",
               "One rewrite, reported once on request 1 and applied again by Compute on requests 2 and 3. The "
               "same 7,000 tokens are priced three ways: write (hot, new content), read (hot, cached by request 1 "
               "without the change), write (cold, after a 20-minute gap). Total $0.05460.",
               "examples.md#2", {"kind": "rewrite", "position": "afterBStarToL", "cache": "hit-hit-miss",
                                 "ttl": "5m", "lifetime": "exact", "replay": "true"}, steps, open_=[BSTAR_SNAP]),
          "rewrite-replay-hot-hot-cold")


# ---------------------------------------------------------------------------------------------
# Tool declarations shared by examples 3, 6 and 7.
TOOL_SIZES = {2: 3500, 9: 2000, 15: 2500, 29: 300}
TOOLS_C = [(f"t{i}", TOOL_SIZES.get(i, 450)) for i in range(30)]  # 20000
TOOLS_F = [(i_t, t) for k, (i_t, t) in enumerate(TOOLS_C) if k not in (2, 9, 15)]  # 12000
TP = "tp:decl-set-7f3a"
SYS3 = [("sys3", 3000)]


def prune_op():
    spans = [{"section": "tools", "from": i, "to": i, "sizeBefore": size(t),
              "sizeAfter": size(0)} for i, t in ((2, 3500), (9, 2000), (15, 2500))]
    return rewrite("op1", "tool-prune", "tools", 2, 8000, 0, TP, spans=spans)


def ex3():
    msgs = [(f"m{i}", 5000) for i in range(4)]
    steps = []
    plan = [(0, "r0", [], "unknown", True), (60, "r1", [("m4", 2000)], "hot", False),
            (1260, "r2", [("m4", 2000), ("m5", 2000)], "cold", False)]
    prev_c = prev_f = 0
    for sec, rid, extra, phase, first in plan:
        m = msgs + extra
        c, f = body(TOOLS_C, SYS3, m), body(TOOLS_F, SYS3, m)
        ct, ft = tokens(TOOLS_C, SYS3, m), tokens(TOOLS_F, SYS3, m)
        if phase == "unknown":
            usage, base = U(write=ft), U(write=ct)
            ops = [eop(TP, saved=TT(cacheWrite5m=8000), note="first request: nothing is cached")]
            boundary = None
        elif phase == "hot":
            usage, base = hot_usage(prev_f, ft), hot_usage(prev_c, ct)
            ops = [eop(TP, saved=TT(cacheRead=8000), note="tools come first, so they are before B*")]
            boundary = P("messages", 3)
        else:
            usage, base = U(write=ft), U(write=ct)
            ops = [eop(TP, saved=TT(cacheWrite5m=8000), note="the entry expired")]
            boundary = None
        steps.append(step(sec, rid, c, f, bps(m), bps(m), [prune_op()] if first else [], usage,
                          expect(phase, base, ops, boundary=boundary)))
        prev_c, prev_f = ct, ft
    write(case("rewrite/tools-spans/first-hot-cold/5m",
               "tool-prune removes 3 tool declarations (3,500 + 2,000 + 2,500 = 8,000 tokens) on the first "
               "request of a thread, as one rewrite with three spans, and the removal applies again on later "
               "requests. The tools come first in the prefix, so the 8,000 tokens are read on a hot request "
               "($0.00240) and written on a cold one ($0.03000). The forwarded prefix never changes, so there is "
               "no cache break.",
               "examples.md#3", {"kind": "rewrite", "position": "tools", "cache": "unknown-hit-miss", "ttl": "5m",
                                 "lifetime": "exact", "spans": "true"}, steps, open_=[BSTAR_SNAP]),
          "rewrite-tools-spans")


# ---------------------------------------------------------------------------------------------
# Example 4: a summary that costs money first.
SU = "cg:summarize:5d21"


def ex4():
    tools, sysb = [("tools4", 4000)], [("sys4", 4000)]
    old = [("m0", 4000), ("m1", 3000), ("m2", 3000), ("m3", 1500), ("m4", 1500)]
    tail = old[3:]
    summary = [("summary", 500)]
    steps = [step(0, "r0", body(tools, sysb, old), body(tools, sysb, old), bps(old), bps(old), [],
                  U(write=21000), expect("unknown", U(write=21000), []))]
    spend = {"id": "op1", "kind": "spend", "operator": "context-guru.summarize", "changeId": SU,
             "call": {"model": "summarizer-model", "params": {"effort": "low"},
                      "usage": {"input": 10000, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0,
                                "output": 500, "costUSD": 0.0125}}}
    rw = rewrite("op2", "context-guru.summarize", "messages", 0, 10000, 500, SU,
                 splice={"at": 0, "removed": 3, "inserted": 1})
    new = []
    prev_c, prev_f = 21000, 21000
    plan = [(60, "A", "hot"), (120, "B", "hot"), (1020, "C", "cold")]
    for k, (sec, rid, phase) in enumerate(plan):
        new.append((f"n{k}", 500))
        c, f = old + new, summary + tail + new
        ct, ft = tokens(tools, sysb, c), tokens(tools, sysb, f)
        if rid == "A":
            usage = U(read=8000, write=ft - 8000)  # the provider reads tools and system only
            base = hot_usage(prev_c, ct)
            ops = [eop(SU, call=spend["call"], note="the summarizer call: priced by the host"),
                   eop(SU, saved=TT(cacheRead=9500), note="would have been read")]
            effects = [effect("break", SU, saved=TT(cacheRead=3500), caused=TT(cacheWrite5m=3500),
                              frm=P("system", 0), to=P("messages", 2),
                              note="the summary and the tail are written, not read; to is in forwarded space (§6.2)")]
            add = [spend, rw]
            boundary = P("messages", 4)
        elif phase == "hot":
            usage, base = hot_usage(prev_f, ft), hot_usage(prev_c, ct)
            ops = [eop(SU, saved=TT(cacheRead=9500), note="applied again; the summary is cached now")]
            effects = []
            add, boundary = [], P("messages", 5)
        else:
            usage, base = U(write=ft), U(write=ct)
            ops = [eop(SU, saved=TT(cacheWrite5m=9500), note="the entry expired")]
            effects = []
            add, boundary = [], None
        steps.append(step(sec, rid, body(tools, sysb, c), body(tools, sysb, f), bps(c), bps(f), add,
                          usage, expect(phase, base, ops, boundary=boundary, effects=effects)))
        prev_c, prev_f = ct, ft
    write(case("rewrite+spend/summary/break-then-payback/5m",
               "A summarizer replaces messages 0 to 2 (10,000 tokens) with a 500-token summary. The call costs "
               "$0.01250 first (summarizer rates). Request A is hot, but the summary changes the start of the "
               "messages, so the provider reads only tools and system: the summary and the tail (3,500 tokens) are "
               "written, a break of 3,500 x ($3.75 - $0.30). A nets -$0.00922. B (hot) saves $0.00285, C (cold, 15 "
               "minutes later) saves $0.03563. The total after three requests, call included, is +$0.01675.",
               "examples.md#4", {"kind": "rewrite+spend", "position": "beforeBStar", "cache": "break-hit-miss",
                                 "ttl": "5m", "lifetime": "exact", "break": "plugin"}, steps, open_=[BSTAR_SNAP]),
          "rewrite-summary-break")


# ---------------------------------------------------------------------------------------------
# Examples 5a and 5b: a router changes one setting. Request 4 left 40,000 tokens cached (tools 5,000,
# system 3,000, messages 32,000). Request 5 adds one 1,000-token message.
def setting_case(cid, description, source, dims, change, model_to, params_to, settings, read, open_):
    tools, sysb = [("tools5", 5000)], [("sys5", 3000)]
    msgs = [(f"m{i}", 8000) for i in range(4)]
    model_from = "large-model"
    steps = [step(0, "req-4", body(tools, sysb, msgs), body(tools, sysb, msgs), bps(msgs), bps(msgs), [],
                  U(write=40000), expect("unknown", U(write=40000), []), session="s3", model=model_from,
                  params={"effort": "high"})]
    msgs5 = msgs + [("m4", 1000)]
    setting = {"id": "op1", "kind": "setting", "operator": "inference-router", "changeId": change,
               "settings": settings}
    moved = 40000 - read  # tokens the provider no longer reads
    start = P("system", 0) if read else None  # the provider still reads up to here
    steps.append(step(60, "req-5", body(tools, sysb, msgs5), body(tools, sysb, msgs5), bps(msgs5), bps(msgs5),
                      [setting], U(read=read, write=41000 - read),
                      expect("hot", U(read=40000, write=1000),
                             [],
                             effects=[effect("break", change, saved=TT(cacheRead=moved), caused=TT(cacheWrite5m=moved),
                                             frm=start, to=P("messages", 3),
                                             note=f"{moved:,} tokens move from read to write")],
                             boundary=P("messages", 3)),
                      session="s3", model=model_to, params=params_to))
    write(case(cid, description, source, dims, steps, open_=open_), cid.replace("/", "-"))


def ex5():
    settings_model = {"model": {"from": "large-model", "to": "small-model", "at": P("tools", 0)}}
    setting_case(
        "setting/model-change/break",
        "A router moves request 5 from large-model to small-model. The provider keeps a cache for each model, so "
        "request 5 reads 0 where request 4 left 40,000 cached. A model is rendered at the start of the prompt, so "
        "the setting is at tools[0] and the break covers the whole cached prefix: 40,000 tokens move from read to "
        "write. They are priced at the small model's rates (the proposed rule, design section 11 question 10): "
        "40,000 x ($1.00 - $0.08) = -$0.03680. The large model's rates would give -$0.13800. The saving of the "
        "cheaper model is not computed (R50).",
        "examples.md#5a", {"kind": "setting", "setting": "model", "position": "tools", "cache": "break",
                           "ttl": "5m", "lifetime": "exact", "break": "setting"},
        "rt:model-7c1a", "small-model", {"effort": "high"}, settings_model, read=0,
        open_=["design §11 question 10: which model's rates price a setting break", BSTAR_SNAP])
    settings_effort = {"effort": {"from": "high", "to": "low", "at": P("messages", 0)}}
    setting_case(
        "setting/effort-change/break-from-messages",
        "A router lowers the effort of request 5 from high to low, on the same large-model. On a model that does not "
        "render the effort ahead of the messages (cached-prefix.md), the tools and the system stay valid: the provider "
        "reads 8,000 tokens and writes the 32,000 tokens of messages 0 to 3 again. The two bodies are identical up to "
        "the new message, so a body comparison finds nothing; the setting carries its position, messages[0]. "
        "32,000 x ($3.75 - $0.30) = -$0.11040.",
        "examples.md#5b", {"kind": "setting", "setting": "effort", "position": "messages", "cache": "break",
                           "ttl": "5m", "lifetime": "exact", "break": "setting"},
        "rt:effort-3b9e", "large-model", {"effort": "low"}, settings_effort, read=8000,
        open_=[BSTAR_SNAP])


# ---------------------------------------------------------------------------------------------
# Examples 6 and 7: four operations on one request, and a change inside the cached prefix.
EXP = "cg:expand:91b0"
MK = "cg:mask:2a77"
COREF = "cg:coref:5e2d"
SIZES6 = {"m0": 4000, "m1": 3000, "m2": 3000, "m3": 4000, "m4": 1000, "m5": 20000, "m6": 2000, "m7": 1500,
          "m8": 1500, "m9": 8000, "m10": 1000, "m11": 1000, "m12": 1000, "m13": 1000}
ADDED = {1: ["m0", "m1"], 2: ["m2"], 3: ["m3", "m4"], 4: ["m5", "m6", "m7", "m8"], 5: ["m9"], 6: ["m10", "m11"],
         7: ["m12", "m13"]}
FWD_OVERRIDE = {"m3": ("m3f", 60), "m9": ("m9f", 900)}
OPS6 = {
    "prune": dict(change=TP, section="tools", idx=None, delta=8000),
    "expand": dict(change=EXP, section="tools", idx=None, delta=-300),
    "mask": dict(change=MK, section="messages", idx=3, delta=3940),
    "cmdfilter": dict(change=CF, section="messages", idx=9, delta=7100),
    "coref": dict(change=COREF, section="messages", idx=5, delta=19950),
}


def op_json(name):
    if name == "prune":
        return prune_op()
    if name == "expand":
        return rewrite("op2", "context-guru.expand", "tools", 27, 0, 300, EXP)
    if name == "mask":
        return rewrite("op3", "context-guru.mask", "messages", 3, 4000, 60, MK)
    if name == "cmdfilter":
        return rewrite("op4", "context-guru.cmdfilter", "messages", 9, 8000, 900, CF)
    return rewrite("op12", "context-guru.coref", "messages", 5, 20000, 50, COREF)


def thread6(last_turn):
    """The turns 1..last_turn of examples 6 and 7, with every expected figure."""
    reported_on = {1: ["prune", "expand"], 3: ["mask"], 5: ["cmdfilter"], 7: ["coref"]}
    expand_tool = [("expand", 300)]
    steps, active = [], []
    c_msgs, f_msgs = [], []
    prev_c = prev_f = prev_n = 0
    for k in range(1, last_turn + 1):
        for n in ADDED[k]:
            c_msgs.append((n, SIZES6[n]))
            f_msgs.append(FWD_OVERRIDE.get(n, (n, SIZES6[n])))
        reported = reported_on.get(k, [])
        now_active = [a for a in active] + reported
        if k == 7:  # coref reduces message 5 inside the cached prefix
            f_msgs[5] = ("m5f", 50)
        c, f = body(TOOLS_C, SYS3, c_msgs), body(TOOLS_F + expand_tool, SYS3, f_msgs)
        ct, ft = tokens(TOOLS_C, SYS3, c_msgs), tokens(TOOLS_F + expand_tool, SYS3, f_msgs)
        hot = k > 1
        phase = "hot" if hot else "unknown"
        ops = []
        for name in reported + active:
            spec = OPS6[name]
            read = hot and (spec["section"] == "tools" or spec["idx"] < prev_n)
            tier = "cacheRead" if read else "cacheWrite5m"
            if spec["delta"] >= 0:
                ops.append(eop(spec["change"], saved={tier: spec["delta"]}, note=name))
            else:
                ops.append(eop(spec["change"], caused={tier: -spec["delta"]}, note=name + ": an added declaration is a cost"))
        base = hot_usage(prev_c, ct) if hot else U(write=ct)
        usage = hot_usage(prev_f, ft) if hot else U(write=ft)
        effects = []
        if k == 7:
            usage = U(read=15300, write=ft - 15300)  # the provider reads tools and system only
            effects.append(effect("break", COREF, saved=TT(cacheRead=19010), caused=TT(cacheWrite5m=19010),
                                  frm=P("system", 0), to=P("messages", 11),
                                  note="19,010 tokens move from read to write"))
        boundary = P("messages", prev_n - 1) if hot else None
        steps.append(step(60 * (k - 1), f"req-{k}", c, f, bps(c_msgs), bps(f_msgs), [op_json(n) for n in reported],
                          usage, expect(phase, base, ops, boundary=boundary, effects=effects)))
        active = now_active
        prev_c, prev_f, prev_n = ct, ft, len(c_msgs)
    return steps


def ex67():
    write(case("rewrite/four-operations/hot/5m",
               "Turn 5 of one thread: cmdfilter is reported on this request, and tool-prune, expand and mask "
               "(reported on turns 1 and 3) are applied again. Turns 1 to 4 build the state. On turn 5 the three "
               "earlier operations change tokens before B* (read, and the added expand tool is a cost) and "
               "cmdfilter changes a new message (write). The four net $0.03012 = the client bill $0.04890 minus "
               "the forwarded bill $0.01878.",
               "examples.md#6", {"kind": "rewrite", "position": "mixed", "cache": "hit", "ttl": "5m",
                                 "lifetime": "exact", "operations": "4"}, thread6(5),
               open_=["design §11 question 12: how Compute knows the three earlier operations are still active", BSTAR_SNAP]),
          "rewrite-four-operations")
    write(case("rewrite/coref-break-inside-prefix/hot/5m",
               "Turn 7: coref reduces message 5 (20,000 tokens to 50), which is inside the prefix that turn 6 "
               "cached. The client did not change, so the change itself breaks the cache. The last breakpoint "
               "before message 5 is the end of system, so the provider reads 15,300 tokens and writes the rest. "
               "coref saves 19,950 x $0.30 = +$0.00599 and the break costs 19,010 x ($3.75 - $0.30) = -$0.06558, "
               "so coref nets -$0.05960 on this turn. The other four operations are applied again as before.",
               "examples.md#7", {"kind": "rewrite", "position": "beforeBStar", "cache": "break", "ttl": "5m",
                                 "lifetime": "exact", "break": "plugin"}, thread6(7),
               open_=["design §11 question 12: how Compute knows the earlier operations are still active", BSTAR_SNAP]),
          "rewrite-coref-break")


# ---------------------------------------------------------------------------------------------
# Example 8: a keep-alive ping and the request it rescues.
KA = "ka:main:w1"


def ex8():
    sysb = [("sys8", 2000)]
    msgs = [("m0", 3000), ("m1", 1000), ("m2", 1000), ("m3", 1000), ("m4", 1000), ("m5", 1000)]
    ping_usage = {"input": 10, "cacheRead": 10000, "cacheWrite": 0, "cacheWrite1h": 0, "output": 1}
    call = {"host": "api.anthropic.com", "model": "agent-model", "usage": ping_usage}
    steps = [step(0, "turn-9", body([], sysb, msgs), body([], sysb, msgs), bps(msgs), bps(msgs), [],
                  U(write=10000), expect("unknown", U(write=10000), []), session="s2")]
    ping = {"version": 1, "requestId": "ping-p1", "session": "s2", "thread": {"id": "main"}, "at": ts(240),
            "billing": {"mode": "api"},
            "ops": [{"id": "op1", "kind": "spend", "operator": "keeper", "changeId": KA, "call": call}]}
    steps.append({"standalone": ping, "usage": U(),
                  "expected": {"ops": [eop(KA, call=call, note="the ping: priced by the host, 3,045 micro-dollars")]}})
    msgs10 = msgs + [("m6", 500)]
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(240), "pingRead": P("messages", 5), "pingWindow": KA}
    steps.append(step(480, "turn-10", body([], sysb, msgs10), body([], sysb, msgs10), bps(msgs10), bps(msgs10), [],
                      U(read=10000, write=500),
                      expect("hot", U(write=10500), [],
                             effects=[effect("rescue", KA, saved=TT(cacheWrite5m=10000), caused=TT(cacheRead=10000),
                                             to=P("messages", 5),
                                             note="without the ping the 5m entry would have expired; write becomes read")]),
                      session="s2", facts=facts))
    write(case("keepalive/ping-rescue/exact/5m",
               "Turn 9 caches 10,000 tokens at minute 20. A keep-alive ping at minute 24 re-reads them (10,000 read, "
               "10 input, 1 output: -$0.00305) and renews the entry. Turn 10 arrives at minute 28, 8 minutes after "
               "turn 9 and so past the 5m TTL, but 4 minutes after the ping and so inside it. Lifetime is exact, so "
               "the expiry without a ping is proven and the rescue is credited: 10,000 x ($3.75 - $0.30) = "
               "+$0.03450. The ping and the rescue share one ChangeID. Total +$0.03146.",
               "examples.md#8", {"kind": "rescue", "position": "beforeBStar", "cache": "rescue", "ttl": "5m",
                                 "lifetime": "exact", "keepalive": "true"}, steps),
          "keepalive-ping-rescue")


# ---------------------------------------------------------------------------------------------
# Shared prefix (R54): example 8, but a sibling thread read the shared head inside the TTL, so it was warm
# without the ping. The rescue credits only what lies past that head.
def shared_prefix(shared, saved, ping_read, ping_tokens, name, text, session):
    sysb = [("sys8", 2000)]
    msgs = [("m0", 3000), ("m1", 1000), ("m2", 1000), ("m3", 1000), ("m4", 1000), ("m5", 1000)]
    ping_usage = {"input": 10, "cacheRead": ping_tokens, "cacheWrite": 0, "cacheWrite1h": 0, "output": 1}
    call = {"host": "api.anthropic.com", "model": "agent-model", "usage": ping_usage}
    steps = [step(0, "turn-9", body([], sysb, msgs), body([], sysb, msgs), bps(msgs), bps(msgs), [],
                  U(write=10000), expect("unknown", U(write=10000), []), session=session)]
    ping = {"version": 1, "requestId": "ping-" + session, "session": session, "thread": {"id": "main"}, "at": ts(240),
            "billing": {"mode": "api"},
            "ops": [{"id": "op1", "kind": "spend", "operator": "keeper", "changeId": KA, "call": call}]}
    steps.append({"standalone": ping, "usage": U(),
                  "expected": {"ops": [eop(KA, call=call, note="the ping: priced by the host")]}})
    msgs10 = msgs + [("m6", 500)]
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(240), "pingRead": ping_read, "pingWindow": KA,
             "sharedWarm": shared}
    effects = []
    if saved:
        effects = [effect("rescue", KA, saved=TT(cacheWrite5m=saved), caused=TT(cacheRead=saved), to=ping_read,
                          note="the ping kept alive only the part no sibling had kept warm")]
    steps.append(step(480, "turn-10", body([], sysb, msgs10), body([], sysb, msgs10), bps(msgs10), bps(msgs10), [],
                      U(read=10000, write=500), expect("hot", U(read=10000 - saved, write=500 + saved), [], effects=effects),
                      session=session, facts=facts))
    write(case(name, text, "examples.md#8", {"kind": "rescue", "position": "beforeBStar", "cache": "rescue",
                                            "ttl": "5m", "lifetime": "exact", "keepalive": "true",
                                            "shared": "sibling"}, steps),
          name.replace("/", "-"))


def shared_prefix_cases():
    head = ("Same as keepalive/ping-rescue/exact/5m, but a sibling thread of the session read the system block "
            "(2,000 tokens) inside the TTL, so that block was warm without the ping. ")
    shared_prefix(P("system", 0), 8000, P("messages", 5), 10000, "keepalive/shared-head-sibling/exact/5m",
                  head + "The rescue credits 8,000 tokens, not 10,000: 8,000 x ($3.75 - $0.30) = +$0.02760.", "s-sh1")
    shared_prefix(P("messages", 5), 0, P("messages", 5), 10000, "keepalive/shared-prefix-all-sibling/exact/5m",
                  "A sibling thread read everything the ping read (through messages[5]) inside the TTL. The entry was "
                  "alive without the ping, so turn 10 is hot and no rescue is credited. The ping stays a cost.",
                  "s-sh2")
    shared_prefix(P("messages", 4), 0, P("messages", 1), 6000, "keepalive/shared-past-ping-read/exact/5m",
                  "The ping read only through messages[1] (6,000 tokens), and a sibling kept warm more than that "
                  "(through messages[4]). Nothing is left to credit, and the figure never goes below zero.",
                  "s-sh3")


# ---------------------------------------------------------------------------------------------
# Example 9: the extract_llm_sweep story. The ask goes out in place of a keep-alive ping, on the warm
# cache; the answer is held; the removal is applied by the first request after the cache expired.
SW = "cg:extract_llm_sweep:7a1c"


def ex9():
    sysb = [("sys9", 2000)]
    base = [("m0", 3000), ("m1", 8000), ("m2", 1000), ("m3", 1000), ("m4", 1000), ("m5", 1000)]  # m1: a spent output
    steps = [step(0, "sw-0", body([], sysb, base), body([], sysb, base), bps(base), bps(base), [],
                  U(write=17000), expect("unknown", U(write=17000), []), session="s3")]
    m6 = base + [("m6", 500)]
    steps.append(step(100, "sw-1", body([], sysb, m6), body([], sysb, m6), bps(m6), bps(m6), [],
                      U(read=17000, write=500), expect("hot", U(read=17000, write=500), []), session="s3"))
    # The ask: the previous turn's forwarded bytes (17,500 tokens) plus a 400-token inventory question.
    # It reads the cache sw-1 left, so the entry is renewed; it replaces the ping. 280 s after sw-1.
    call = {"host": "api.anthropic.com", "model": "agent-model",
            "usage": {"input": 400, "cacheRead": 17500, "cacheWrite": 0, "cacheWrite1h": 0, "output": 200}}
    ask = {"version": 1, "requestId": "sw-ask-1", "session": "s3", "thread": {"id": "main"}, "at": ts(380),
           "billing": {"mode": "api"},
           "ops": [{"id": "op1", "kind": "spend", "operator": "context-guru.extract_llm_sweep", "changeId": SW,
                    "call": call}]}
    steps.append({"standalone": ask, "usage": U(),
                  "expected": {"ops": [eop(SW, call=call, note="the ask, sent in place of the ping: priced by the host, 9,450 micro-dollars")]}})
    # The user leaves; the keep-alive budget ends; the entry expires. sw-2 arrives 1,420 s after the ask.
    c2 = m6 + [("m7", 500)]
    f2 = [base[0], ("m1f", 100)] + base[2:] + [("m6", 500), ("m7", 500)]
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(380), "pingRead": P("messages", 6)}
    rw = rewrite("op2", "context-guru.extract_llm_sweep", "messages", 1, 8000, 100, SW)
    steps.append(step(1800, "sw-2", body([], sysb, c2), body([], sysb, f2), bps(c2), bps(f2), [rw],
                      U(write=10100),
                      expect("cold", U(write=18000),
                             [eop(SW, saved=TT(cacheWrite5m=7900),
                                  note="the held verdict is applied on the cold request; the output would have been written")]),
                      session="s3", facts=facts))
    write(case("rewrite+spend/extract-llm-sweep/ask-then-cold-apply/5m",
               "A spent 8,000-token tool output (message 1) sits in a 17,500-token thread. At minute 6:20 the "
               "keep-alive ping is replaced by the sweep's ask: the previous turn's forwarded body plus a "
               "400-token question, so it reads 17,500 tokens from the warm cache and renews the entry (400 "
               "input, 17,500 read, 200 output: -$0.00945). The model says drop; the verdict is held and changes "
               "no request while the cache is warm, because a removal inside the cached prefix would be a break. "
               "The keep-alive ends and the entry expires. The next request arrives 23 minutes later, past the "
               "5m TTL of both the last request and the ask, so no rescue is credited and nothing is cached: "
               "the held verdict is applied and the 7,900 removed tokens (8,000 to a 100-token descriptor with a "
               "<<cg:HASH>> marker) are priced as cache writes, 7,900 x $3.75 = +$0.02963. The ask and the "
               "removal share one ChangeID. Total +$0.02018.",
               "advanced-offload.md#extract_llm_sweep",
               {"kind": "rewrite+spend", "position": "beforeBStar", "cache": "hit-ask-miss", "ttl": "5m",
                "lifetime": "exact", "keepalive": "true", "sweep": "true"}, steps),
          "rewrite-spend-sweep-ask-then-cold-apply")


# ---------------------------------------------------------------------------------------------
# Cases derived from issues (docs/issue_cases.md). Each id's source names the issue.
ISSUES = "issue_cases.md"


def sim(sec, rid, C, F, ops, phase, add=(), prev=(0, 0), effects=None, usage=None, base=None,
        boundary=None, cbp=None, fbp=None, unpriced=None, checks=None, compaction=None, ended=None, **kw):
    """One request step. C and F are (tools, system, msgs) of the client and forwarded bodies.
    hot: the provider reads the previous body's tokens and writes the rest; unknown/cold: writes all."""
    ct, ft = tokens(*C), tokens(*F)
    if phase == "hot":
        u, b = hot_usage(prev[1], ft), hot_usage(prev[0], ct)
    else:
        u, b = U(write=ft), U(write=ct)
    if phase == "unknown" and unpriced is None:  # §5.2 row 6 and §6.4: a first request has no known boundary
        unpriced = [{"reason": "penalty_unknown"}]  # request-level: the boundary is unknown, no operation owns it
    e = expect(phase, base or b, ops, boundary=boundary, unpriced=unpriced, effects=effects, checks=checks,
               compaction=compaction, ended=ended)
    return step(sec, rid, body(*C), body(*F), bps(C[2]) if cbp is None else cbp, bps(F[2]) if fbp is None else fbp, list(add),
                usage or u, e, compaction=compaction, **kw)


def BP(*pairs, ttl="5m"):
    return [{"pos": P(sec, idx), "ttl": ttl} for sec, idx in pairs]


def ping_step(sec, session, change, call, window_unpriced=None):
    ping = {"version": 1, "requestId": f"ping-{sec}", "session": session, "thread": {"id": "main"}, "at": ts(sec),
            "billing": {"mode": "api"},
            "ops": [{"id": "op1", "kind": "spend", "operator": "keeper", "changeId": change, "call": call}]}
    exp = {"ops": [eop(change, call=call)]}
    if window_unpriced:
        exp["unpriced"] = window_unpriced
    return {"standalone": ping, "usage": U(), "expected": exp}


def DIMS(kind, position, cache, **kw):
    d = {"kind": kind, "position": position, "cache": cache, "ttl": "5m", "lifetime": "exact"}
    d.update(kw)
    return d


SNAP_CASES = {"rewrite/searchfold-inside-prefix/hot/5m", "rewrite/decision-flips-inside-prefix/hot/5m",
              "rewrite/replay-at-new-position/hot/5m", "rewrite/break-then-payback-turns/hot/5m",
              "rewrite/restore-in-place/hot/5m", "state/session-isolation",
              "split/marker-not-in-system", "check/changeid-shared-between-operators"}


def write_issue(cid, issue, description, dims, steps, open_=None):
    if cid in SNAP_CASES and BSTAR_SNAP not in (open_ or []):
        open_ = (open_ or []) + [BSTAR_SNAP]
    if cid == "break/client-edit-inside-prefix/hot/5m":  # B* is messages[9] under either reading
        open_ = [o for o in (open_ or []) if o != BSTAR_SNAP] or None
    write(case(cid, description, f"{ISSUES}#{issue}", dims, steps, open_=open_), cid.replace("/", "-").replace("+", "-"))




# Case 1 (#580): a ping that missed and wrote the entry restarts the clock.
def c01():
    sysb, m0 = [("sys", 2000)], [("m0", 18000)]
    KAW = "ka:main:w2"
    call = {"host": "api.anthropic.com", "model": "agent-model",
            "usage": {"input": 10, "cacheRead": 0, "cacheWrite": 20000, "cacheWrite1h": 0, "output": 1}}
    m1 = m0 + [("m1", 500)]
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(360), "pingRead": P("messages", 0), "pingWindow": KAW}
    steps = [sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown", session="s-kp"),
             ping_step(360, "s-kp", KAW, call),
             sim(540, "r1", ([], sysb, m1), ([], sysb, m1), [], "hot", prev=(20000, 20000),
                 usage=U(read=20000, write=500), base=U(write=20500), facts=facts, session="s-kp",
                 effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=20000), caused=TT(cacheRead=20000),
                                 to=P("messages", 0),
                                 note="the ping missed and wrote the entry: the entry is alive from minute 6")])]
    write_issue("keepalive/ping-miss-restarts-clock/5m", 580,
                "The entry (20,000 tokens) expired before the ping at minute 6, so the ping read 0 and wrote it again. "
                "Turn 1 arrives at minute 9: past the TTL measured from turn 0, inside it measured from the ping. The "
                "entry gap runs from LastPing, so the phase is hot and the rescue is credited: 20,000 x ($3.75 - $0.30) "
                "= +$0.06900. The ping costs 20,000 x $3.75 + 10 x $3.00 + 1 x $15.00 = -$0.07505, so the window nets "
                "-$0.00605: a ping that missed pays for itself at the read price only.",
                DIMS("rescue", "beforeBStar", "rescue", keepalive="true", ping="missed-and-wrote"), steps,
                open_=["design §4.4 rule 4 takes the smaller of what the last ping READ and what the request read. A ping "
                       "that missed read 0 but re-created the entry, so read literally the rescue is 0. This case credits "
                       "the extent the ping covered (PingRead). The rule needs to say so",
                       "design §4.1 defines PingRead as the extent the ping READ, which is nil for a ping that missed; "
                       "this case sets it to messages[0] so that row 2 of the phase table matches. Both need amending"])


# Case 2 (#532): a ping whose answer never arrived.
def c02():
    sysb = [("sys", 2000)]
    m0, m1 = [("m0", 197103)], [("m0", 197103), ("m1", 387)]
    KAW = "ka:main:w3"
    call = {"host": "api.anthropic.com", "model": "agent-model",
            "usage": {"input": 0, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": 0}}
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(271), "pingRead": P("messages", 0), "pingWindow": KAW}
    steps = [sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown", session="s-slow"),
             ping_step(271, "s-slow", KAW, call, [{"opId": "op1", "reason": "ping_result_unknown"}]),
             sim(526, "r1", ([], sysb, m1), ([], sysb, m1), [], "hot", prev=(199103, 199103),
                 usage=U(read=199103, write=387), base=U(write=199490), facts=facts, session="s-slow",
                 effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=199103), caused=TT(cacheRead=199103),
                                 to=P("messages", 0), note="proved by the read on this request")])]
    write_issue("keepalive/ping-timeout-unknown-result/5m", 532,
                "The proxy gave up on a ping after 60 seconds, so the host has no usage for it. The next turn comes 8 "
                "minutes 46 seconds after the last one and reads 199,103 of 199,490 tokens. The ping is reported as a "
                "spend with empty usage and the library marks it unpriced (ping_result_unknown). The rescue is credited "
                "from the read on the request, which proves the entry was alive. The window shows a rescue and an "
                "unpriced ping, never a rescue with a silent zero cost.",
                DIMS("rescue", "beforeBStar", "rescue", keepalive="true", ping="timed-out"), steps,
                open_=["new: the design has no form for a ping whose usage is unknown. This case assumes the host reports "
                       "an empty-usage spend and the library answers Unpriced ping_result_unknown (option c of issue_cases.md case 2)",
                       "design §4.4 rule 4 credits the smaller of what the last ping read and what the request read. The "
                       "ping's read is unknown here, so the credit of 199,103 tokens rests on the request's read alone"])


# Cases 3 and 4 (#496): the body changes inside the cached prefix on a warm request.
SF = "cg:searchfold:6b02"


def c03_04():
    tools, sysb = [("tools", 6000)], [("sys", 2000)]
    base = [(f"m{i}", 2500) for i in range(10)] + [("m10", 3000)] + [(f"m{i}", 2800) for i in range(11, 16)]
    bp = lambda last: BP(("system", 0), ("messages", 9), ("messages", last))
    s0 = sim(0, "r0", (tools, sysb, base), (tools, sysb, base), [], "unknown", cbp=bp(15), fbp=bp(15))
    tail = [("m16", 2000)]
    fwd = [m if m[0] != "m10" else ("m10f", 500) for m in base] + tail
    op = rewrite("o1", "context-guru.searchfold", "messages", 10, 3000, 500, SF)
    s1 = sim(40, "r1", (tools, sysb, base + tail), (tools, sysb, fwd), [eop(SF, saved=TT(cacheRead=2500))],
             "hot", add=[op], cbp=bp(16), fbp=bp(16), usage=U(read=33000, write=16500), prev=(50000, 50000),
             boundary=P("messages", 15),
             effects=[effect("break", SF, saved=TT(cacheRead=14500), caused=TT(cacheWrite5m=14500),
                             frm=P("messages", 9), to=P("messages", 15),
                             note="the marker and everything after it are written, not read")])
    write_issue("rewrite/searchfold-inside-prefix/hot/5m", 496,
                "A warm request (40 seconds after the last). searchfold shortens message 10, a 3,000-token output "
                "inside the cached prefix, to 500 tokens. The client breakpoints are the end of system, message 9 and "
                "the last message, so the provider reads through message 9 (33,000 tokens) and writes the other 16,500. "
                "searchfold saves 2,500 x $0.30 = +$0.00075 and breaks the cache on 14,500 tokens, 14,500 x ($3.75 - "
                "$0.30) = -$0.05003: net -$0.04928. The break carries searchfold's ChangeID, so the cost is visible "
                "where the dashboard in issue 496 showed a hit.",
                DIMS("rewrite", "beforeBStar", "break", **{"break": "plugin"}), [s0, s1])

    cl = [m if m[0] != "m10" else ("m10c", 500) for m in base] + tail
    s1b = sim(40, "r1", (tools, sysb, cl), (tools, sysb, cl), [], "hot", cbp=bp(16), fbp=bp(16),
              usage=U(read=33000, write=16500), base=U(read=33000, write=16500), boundary=P("messages", 9),
              effects=[effect("break", "", to=P("messages", 9),
                              note="the client changed message 10 itself: B* is the breakpoint at message 9, "
                                   "so the break moves no tokens")])
    write_issue("break/client-edit-inside-prefix/hot/5m", 496,
                "The same usage as the searchfold case (33,000 read, 16,500 written), but no context operation touched "
                "the cached part: the client shortened message 10 itself. The break is on the record with an empty "
                "ChangeID (R31) and, because B* is the breakpoint at message 9, moves no tokens (§5.1, §6.2): the "
                "baseline equals the bill and no operator gets a saving or a cost.",
                DIMS("none", "beforeBStar", "break", **{"break": "client"}), [s0, s1b], open_=[BSTAR_SNAP])


# Case 5 (#493): a decision flips on a cached message.
DD = "cg:dedup:4c18"


def c05():
    sysb = [("sys", 2000)]
    m = [("m0", 3000)] + [(f"m{i}", 2000) for i in range(1, 7)]
    bp = lambda last: BP(("system", 0), ("messages", 4), ("messages", last))
    s1 = sim(0, "r1", ([], sysb, m), ([], sysb, m), [], "unknown", cbp=bp(6), fbp=bp(6))
    c2 = m + [("m7", 1000)]
    f2 = [x if x[0] != "m5" else ("m5f", 100) for x in c2]
    s2 = sim(30, "r2", ([], sysb, c2), ([], sysb, f2), [eop(DD, saved=TT(cacheRead=1900))], "hot",
             add=[rewrite("o1", "context-guru.dedup", "messages", 5, 2000, 100, DD)], prev=(17000, 17000),
             usage=U(read=13000, write=3100), cbp=bp(7), fbp=bp(7), boundary=P("messages", 6),
             effects=[effect("break", DD, saved=TT(cacheRead=2100), caused=TT(cacheWrite5m=2100),
                             frm=P("messages", 4), to=P("messages", 6))])
    c3, f3 = c2 + [("m8", 1000)], f2 + [("m8", 1000)]
    s3 = sim(70, "r3", ([], sysb, c3), ([], sysb, f3), [eop(DD, saved=TT(cacheRead=1900))], "hot",
             prev=(18000, 16100), cbp=bp(8), fbp=bp(8), boundary=P("messages", 7))
    c4 = c3 + [("m9", 1000)]
    s4 = sim(100, "r4", ([], sysb, c4), ([], sysb, c4), [], "hot", prev=(19000, 17100),
             usage=U(read=13000, write=7000), cbp=bp(9), fbp=bp(9), boundary=P("messages", 8),
             effects=[effect("break", "", saved=TT(cacheRead=6000), caused=TT(cacheWrite5m=6000), baseline=False,
                             frm=P("messages", 4), to=P("messages", 8),
                             note="dedup stopped replacing message 5: the original returns inside the prefix")])
    write_issue("rewrite/decision-flips-inside-prefix/hot/5m", 493,
                "Message 5 repeats message 2. Request 1 runs with dedup off. On request 2 dedup turns on and replaces "
                "message 5 (2,000 to 100 tokens), which is inside the cached prefix: it saves 1,900 x $0.30 and breaks "
                "2,100 tokens, net -$0.00668. Request 3 replays the same replacement: no break, +$0.00057. On request 4 "
                "dedup stops (its settings changed), the original returns inside the cached prefix and 6,000 tokens move "
                "from read to write: -$0.02070. Dedup reported nothing, and the change has ended, so the break has no ChangeID (§5.2 row 1).",
                DIMS("rewrite", "beforeBStar", "break-hit-break", **{"break": "plugin", "replay": "true"}), [s1, s2, s3, s4])


# Case 6 (#199): a replay at a new position that was never compacted.
EL, EL2 = "cg:extract_llm:3d10", "cg:extract_llm:b9f1"


def c06():
    sysb = [("sys", 2000)]
    head = [("m0", 2000), ("m1", 2000), ("m2", 2000)]
    cm0 = head + [("m3", 6000)]
    fm0 = head + [("m3f", 500)]
    s0 = sim(0, "r0", ([], sysb, cm0), ([], sysb, fm0), [eop(EL, saved=TT(cacheWrite5m=5500))], "unknown",
             add=[rewrite("o1", "context-guru.extract_llm", "messages", 3, 6000, 500, EL)])
    mid = [(f"m{i}", 1000) for i in range(4, 12)]
    mid = [m if m[0] != "m8" else ("m8", 6000) for m in mid]  # the same 6,000-token output, second occurrence
    c1, f1 = cm0 + mid + [("m12", 1000)], fm0 + mid + [("m12", 1000)]
    bp = lambda last: BP(("system", 0), ("messages", 11), ("messages", last))
    # the provider reads only up to a breakpoint (§5.1), so the 12,500 tokens it read end at one (§6.2)
    bp7 = lambda last: BP(("system", 0), ("messages", 7), ("messages", 11), ("messages", last))
    s1 = sim(60, "r1", ([], sysb, c1), ([], sysb, f1), [eop(EL, saved=TT(cacheRead=5500))], "hot",
             prev=(14000, 8500), cbp=bp(12), fbp=bp(12), boundary=P("messages", 3))
    c2 = c1 + [("m13", 1000)]
    mid2 = [m if m[0] != "m8" else ("m8f", 500) for m in mid]
    f2 = fm0 + mid2 + [("m12", 1000), ("m13", 1000)]
    s2 = sim(120, "r2", ([], sysb, c2), ([], sysb, f2),
             [eop(EL2, saved=TT(cacheRead=5500)), eop(EL, saved=TT(cacheRead=5500))], "hot",
             add=[rewrite("o2", "context-guru.extract_llm", "messages", 8, 6000, 500, EL2)], prev=(28000, 22500),
             usage=U(read=12500, write=5500), cbp=bp7(13), fbp=bp7(13), boundary=P("messages", 12),
             effects=[effect("break", EL2, saved=TT(cacheRead=4500), caused=TT(cacheWrite5m=4500),
                             frm=P("messages", 7), to=P("messages", 12),
                             note="the break starts at the read extent (§6.2). Message 8 was never compacted, so the provider holds the original and "
                                  "everything after it is written again")])
    write_issue("rewrite/replay-at-new-position/hot/5m", 199,
                "The same 6,000-token output appears at message 3 and at message 8. extract_llm compacted message 3 "
                "when it arrived, so the provider holds the compacted form there and the replay is free of any break. "
                "Message 8 was not compacted on arrival (the operator was throttled). On request 2 the operator compacts "
                "it inside the cached prefix: a second change with its own ChangeID saves 5,500 x $0.30 = +$0.00165 and "
                "breaks the 4,500 tokens from message 8 to the end of the prefix, 4,500 x $3.45 = -$0.01553: the break "
                "is larger than the saving, which is the cost issue 199 hid. The replay of the first change stays "
                "break-free.",
                DIMS("rewrite", "beforeBStar", "hit-hit-break", **{"break": "plugin", "replay": "true"}), [s0, s1, s2])


# Case 7 (#197): two operators report one ChangeID.
def c07():
    sysb = [("sys", 2000)]
    m0 = [("m0", 2000)]
    CH = "cg:res:5c1d"
    s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown")
    c1 = m0 + [("m1", 6000), ("m2", 7000)]
    f1 = m0 + [("m1f", 500), ("m2f", 800)]
    s1 = sim(40, "r1", ([], sysb, c1), ([], sysb, f1),
             [eop(CH, saved=TT(cacheWrite5m=5500)), eop(CH, saved=TT(cacheWrite5m=6200))], "hot",
             add=[rewrite("o1", "context-guru.extract_llm", "messages", 1, 6000, 500, CH),
                  rewrite("o2", "context-guru.extract_llm_sweep", "messages", 2, 7000, 800, CH)],
             prev=(4000, 4000), boundary=P("messages", 0), checks=["unique_change"])
    write_issue("check/changeid-shared-between-operators", 197,
                "extract_llm and extract_llm_sweep share the cg:res: namespace, so both report the same ChangeID for "
                "different messages and different sizes. The library prices each operation by its own size (5,500 and "
                "6,200 tokens at the write tier) and the unique_change check (§6.5 check 3) fails and is reported. A failed check does not stop "
                "pricing.",
                DIMS("rewrite", "afterBStar", "hit", operations="2", check="unique_change"), [s0, s1])
    # The negative variant: one operator reports the same ChangeID on two requests. That is a replay.
    rc0, rf0 = m0 + [("m1", 6000)], m0 + [("m1f", 500)]
    r0 = sim(0, "r0", ([], sysb, rc0), ([], sysb, rf0), [eop(CH, saved=TT(cacheWrite5m=5500))], "unknown",
             add=[rewrite("o1", "context-guru.extract_llm", "messages", 1, 6000, 500, CH)])
    rc1, rf1 = rc0 + [("m3", 1000)], rf0 + [("m3", 1000)]
    r1 = sim(40, "r1", ([], sysb, rc1), ([], sysb, rf1), [eop(CH, saved=TT(cacheRead=5500))], "hot",
             prev=(10000, 4500), boundary=P("messages", 1))
    write_issue("check/changeid-same-operator-replay", 197,
                "The negative variant of the shared-ChangeID case: one operator reports the same ChangeID on two "
                "requests. That is a replay (§4.6), so the unique_change check does not fail and the second request "
                "saves the same 5,500 tokens at the read tier.",
                DIMS("rewrite", "beforeBStar", "hit", operations="1", replay="true"), [r0, r1])


# Case 8 (#214): model calls whose removal was refused.
def spend_call(inp, out, cost):
    return {"model": "summarizer-model", "params": {"effort": "low"},
            "usage": {"input": inp, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": out, "costUSD": cost}}


def c08():
    sysb, m0 = [("sys", 2000)], [("m0", 4000)]
    CH = "cg:extract_llm:77e0"
    call = spend_call(6000, 400, 0.008)
    sp = lambda i, ch: {"id": f"op{i}", "kind": "spend", "operator": "context-guru.extract_llm", "changeId": ch, "call": call}
    c1 = m0 + [("m1", 1000)]
    s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown")
    s1 = sim(40, "r1", ([], sysb, c1), ([], sysb, c1), [eop(CH, call=call)], "hot", add=[sp(1, CH)],
             prev=(6000, 6000), boundary=P("messages", 0))
    write_issue("spend/without-rewrite/refused", 214,
                "extract_llm paid for one model call (6,000 input, 400 output: $0.00800 at summarizer rates) but the "
                "reserve was full, so the pipeline refused the removal. No rewrite is reported. The spend is priced, "
                "nothing is saved and nothing breaks: the request's net is exactly minus the call.",
                DIMS("spend", "none", "hit"), [s0, s1],
                open_=["design §4.1 says a spend's ChangeID names the change its call created, and R47 gives a spend that "
                       "creates no saving an empty ChangeID. The removal was refused, so the empty form may be the right "
                       "one. This case keeps a ChangeID; the numbers do not depend on it"])
    steps = []
    for k, sess in enumerate(("s-a", "s-b", "s-c")):
        steps.append(sim(k, f"r0-{sess}", ([], sysb, m0), ([], sysb, m0), [], "unknown", session=sess))
    for k, sess in enumerate(("s-a", "s-b", "s-c")):
        ch = CH  # rule 6: the same operator and content give the same ChangeID in every session
        steps.append(sim(40 + k, f"r1-{sess}", ([], sysb, c1), ([], sysb, c1),
                         [eop(ch, call=call), eop(ch, call=call)], "hot", add=[sp(1, ch), sp(2, ch)],
                         prev=(6000, 6000), boundary=P("messages", 0), session=sess))
    write_issue("spend/without-rewrite/refused-three-sessions", 214,
                "Three sessions share one process and one full reserve. Each pays two calls and each loses both. Every "
                "session's net is minus its own two calls ($0.01600) and no session carries another's cost.",
                DIMS("spend", "none", "hit", sessions="3"), steps)


# Case 9 (#232): a break that pays back over many turns.
CO = "cg:coref:7a03"


def c09():
    sysb = [("sys", 2000)]
    head = [("m0", 4000)]
    xo, tail = [("m1", 20000)], [("m2", 10000), ("m3", 10000)]
    bp = lambda last: BP(("system", 0), ("messages", 0), ("messages", last))
    c0 = head + xo + tail
    steps = [sim(0, "r0", ([], sysb, c0), ([], sysb, c0), [], "unknown", cbp=bp(3), fbp=bp(3))]
    fwd = head + [("m1f", 100)] + tail
    pc, pf = tokens(sysb, c0), tokens(sysb, fwd) + 19900 - 19900
    c, f = c0 + [("m4", 1000)], fwd + [("m4", 1000)]
    op = rewrite("o1", "context-guru.coref", "messages", 1, 20000, 100, CO)
    steps.append(sim(40, "r1", ([], sysb, c), ([], sysb, f), [eop(CO, saved=TT(cacheRead=19900))], "hot", add=[op],
                     prev=(46000, 46000), cbp=bp(4), fbp=bp(4), usage=U(read=6000, write=21100),
                     boundary=P("messages", 3),
                     effects=[effect("break", CO, saved=TT(cacheRead=20100), caused=TT(cacheWrite5m=20100),
                                     frm=P("messages", 0), to=P("messages", 3))]))
    pc, pf = tokens(sysb, c), tokens(sysb, f)
    for k in range(2, 13):  # 11 hot replays: the break is repaid on the 11th
        c, f = c + [(f"m{k + 3}", 1000)], f + [(f"m{k + 3}", 1000)]
        steps.append(sim(40 + 60 * (k - 1), f"r{k}", ([], sysb, c), ([], sysb, f),
                         [eop(CO, saved=TT(cacheRead=19900))], "hot", prev=(pc, pf), cbp=bp(len(c) - 1),
                         fbp=bp(len(f) - 1), boundary=P("messages", len(c) - 2)))
        pc, pf = tokens(sysb, c), tokens(sysb, f)
    c, f = c + [("m16", 1000)], f + [("m16", 1000)]
    steps.append(sim(40 + 60 * 11 + 1500, "r13", ([], sysb, c), ([], sysb, f),
                     [eop(CO, saved=TT(cacheWrite5m=19900))], "cold", cbp=bp(len(c) - 1), fbp=bp(len(f) - 1),
                     effects=[effect("break", "", note="a cold request: the break moves no tokens (§5.2 row 5)")]))
    write_issue("rewrite/break-then-payback-turns/hot/5m", 232,
                "coref reduces a 20,000-token output (message 1) inside the cached prefix with 20,000 tokens after it. "
                "Request 1 saves 19,900 x $0.30 = +$0.00597 and breaks 20,100 tokens, 20,100 x $3.45 = -$0.06935: net "
                "-$0.06338. Each of the next 11 hot requests saves +$0.00597. The cumulative net is still -$0.00368 after "
                "10 replays and turns positive on the 11th (+$0.00230). A cold request 25 minutes later saves 19,900 x "
                "$3.75 = +$0.07463, because the content would have been written.",
                DIMS("rewrite", "beforeBStar", "break-then-hits-then-miss", **{"break": "plugin", "replay": "true"}), steps)


# Case 10 (#47): the state is lost, the request already reads from the cache.
def c10():
    sysb = [("sys", 2000)]
    C = [("m0", 4000), ("m1", 6000), ("m2", 6000), ("m3", 6000), ("m4", 2000)]
    F = [("m0", 4000), ("m1f", 500), ("m2", 6000), ("m3", 6000), ("m4", 2000)]
    CH = "cg:extract:1a40"
    s = sim(0, "r0", ([], sysb, C), ([], sysb, F), [eop(CH, saved=TT(cacheRead=5500))], "hot",
            add=[rewrite("o1", "context-guru.extract", "messages", 1, 6000, 500, CH)],
            usage=U(read=18500, write=2000), base=U(read=24000, write=2000),
            unpriced=[{"reason": "penalty_unknown"}], session="s-restart",
            # the provider reads only up to a breakpoint (§5.1), so the 18,500 tokens it read end at one
            cbp=BP(("system", 0), ("messages", 3), ("messages", 4)), fbp=BP(("system", 0), ("messages", 3), ("messages", 4)))
    write_issue("boundary/state-lost-reads-cache", 47,
                "The proxy restarted: the Tracker has no history for the thread. The replacement of message 1 (6,000 to "
                "500 tokens) is replayed from the store at depth, and the provider reads 18,500 tokens, so it holds the "
                "compacted form. The usage proves the entry (§5.2), so the phase is hot and the saved tokens are priced "
                "as a read (5,500 x $0.30 = +$0.00165), never as a write. No break can be charged without a previous "
                "fingerprint, so penalty_unknown is reported: the net is not a measured figure.",
                DIMS("rewrite", "beforeBStar", "unknown-read-by-evidence", boundary="unknown"), [s],
                open_=["design §5.2 row 6 says the boundary is unknown, and the evidence rule says to use the extent the read "
                       "covers, which would make it known and §6.4 would not apply. This case takes row 6 and §6.4"])


# Case 11 (#555): the client body's size follows from the result.
def c11():
    sysb = [("sys", 2000)]
    C = [("m0", 4000), ("m1", 10000), ("m2", 10000), ("m3", 10000), ("m4", 2000)]
    F = [("m0", 4000), ("m1f", 1000), ("m2f", 1000), ("m3f", 6000), ("m4", 2000)]
    A, B, D = "cg:mask:aa01", "cg:mask:aa02", "cg:cmdfilter:aa03"
    s = sim(0, "r0", ([], sysb, C), ([], sysb, F),
            [eop(A, saved=TT(cacheWrite5m=9000)), eop(B, saved=TT(cacheWrite5m=9000)),
             eop(D, saved=TT(cacheWrite5m=4000))], "unknown",
            add=[rewrite("o1", "context-guru.mask", "messages", 1, 10000, 1000, A),
                 rewrite("o2", "context-guru.mask", "messages", 2, 10000, 1000, B),
                 rewrite("o3", "context-guru.cmdfilter", "messages", 3, 10000, 6000, D)])
    write_issue("conservation/client-size-from-result", 555,
                "Three operators remove 22,000 of the 38,000 tokens the client sent, so the provider bills 16,000. The "
                "fill a size gate wants (38,000) is not billed, but it follows from the result with no new field: billed "
                "+ sum of Saved - sum of Caused = 16,000 + 22,000 - 0. cases_test.go pins that identity for every case.",
                DIMS("rewrite", "afterBStar", "miss", operations="3"), [s],
                open_=["new: whether Result should expose the client-side token total (issue 555) or leave the sum to the "
                       "trigger. The identity holds either way"])


# Case 12 (#175): one session's state does not reach another.
def c12():
    sysb = [("sys", 2000)]
    CH = "cg:extract:1a40"
    C0 = [("m0", 3000), ("m1", 8000)]
    F0 = [("m0", 3000), ("m1f", 500)]
    op = rewrite("o1", "context-guru.extract", "messages", 1, 8000, 500, CH)
    a0 = sim(0, "ra0", ([], sysb, C0), ([], sysb, F0), [eop(CH, saved=TT(cacheWrite5m=7500))], "unknown", add=[op],
             session="s-a")
    C1, F1 = C0 + [("m2", 1000)], F0 + [("m2", 1000)]
    a1 = sim(40, "ra1", ([], sysb, C1), ([], sysb, F1), [eop(CH, saved=TT(cacheRead=7500))], "hot",
             prev=(13000, 5500), boundary=P("messages", 1), session="s-a")
    b0 = sim(50, "rb0", ([], sysb, C0), ([], sysb, F0), [eop(CH, saved=TT(cacheWrite5m=7500))], "unknown", add=[op],
             session="s-b")
    write_issue("state/session-isolation", 175,
                "Session A and session B hold the same 8,000-token output and the operator derives the same ChangeID "
                "for it. A's change is active in A's Tracker. B's first request reports the change as new and must be "
                "priced alone: one op, a write-tier saving, no unique_change failure. ThreadKey includes the session, so "
                "nothing of A's state reaches B.",
                DIMS("rewrite", "afterBStar", "miss", sessions="2"), [a0, a1, b0])


# One session, three threads: a change on main is priced only on main.
def c_threads():
    side, sub = {"id": "p:side", "parent": "main"}, {"id": "a:sub", "parent": "main"}
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    C2, F2 = C1 + [("m10", 500)], F1 + [("m10", 500)]
    first = lambda sec, rid, th: sim(sec, rid, ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown", thread=th)
    new = lambda sec, rid, th: sim(sec, rid, ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000),
                                   note="new content on this thread, would have been written")], "hot",
                                   add=[cmdfilter_op()], prev=(30000, 30000), boundary=P("messages", 8), thread=th)
    again = lambda sec, rid, th: sim(sec, rid, ([], SYS1, C2), ([], SYS1, F2), [eop(CF, saved=TT(cacheRead=7000),
                                     note="applied again on the thread that reported it")], "hot",
                                     prev=(38000, 31000), boundary=P("messages", 9), thread=th)
    steps = [first(0, "main-0", None), new(10, "main-1", None),
             first(20, "side-0", side), first(30, "sub-0", sub),
             again(70, "main-2", None),
             new(80, "side-1", side), new(90, "sub-1", sub),
             again(150, "side-2", side)]
    write(case("threads/change-stays-on-its-thread",
               "One session, three threads: main, p:side and a:sub. main reports a cmdfilter change and then applies "
               "it again, +$0.02625 as new and +$0.00210 when applied again. The side thread and the subagent send the "
               "same body and carry no change, so main's change is priced on neither: they have no op. Later each "
               "reports the same ChangeID in its own request. Each prices it as new (a write), and no unique_change "
               "failure is raised, because that check runs inside one request. The side thread then applies it again, "
               "as a read. Each thread keeps its own state, so main's change is never applied to another thread. (The "
               "Version lock of each thread is checked by TestThreadsOfOneSessionKeepTheirOwnChangesAndVersions, not by "
               "a case.)",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", threads="3"), steps),
          "threads-change-stays-on-its-thread")


# A client compaction ends the replay of an earlier change (§4.3). Nothing is replayed across it, and a change
# reported after it is new work, priced in full. One thread: a change, a replay, the compaction, then a new change.
def c_client_compaction():
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    C2, F2 = C1 + [("m10", 500)], F1 + [("m10", 500)]
    # The client compacts: a summary replaces message 0 and the messages after it, and the last two stay.
    S = [("sum", 1500), ("m9k", 1500), ("m10k", 500)]
    S2 = S + [("m11", 500)]
    C3, F3 = S2 + [("m12", 8000)], S2 + [("m12f", 1000)]
    CF2 = "cg:cmdfilter:9b17"
    op2 = rewrite("o2", "context-guru.cmdfilter", "messages", 4, 8000, 1000, CF2)
    bp = lambda last: BP(("system", 0), ("messages", last))
    steps = [
        sim(0, "r0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown"),
        sim(10, "r1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000),
            note="new content, would have been written")], "hot", add=[cmdfilter_op()], prev=(30000, 30000),
            boundary=P("messages", 8)),
        sim(70, "r2", ([], SYS1, C2), ([], SYS1, F2), [eop(CF, saved=TT(cacheRead=7000),
            note="applied again; the client's prefix would be cached")], "hot", prev=(38000, 31000),
            boundary=P("messages", 9)),
        # r3: the client compacted. Message 0 is new, so the provider reads only the system block (2,000 tokens)
        # and writes the other 3,500. The old change has ended: it has no operation and no saving.
        sim(130, "r3", ([], SYS1, S), ([], SYS1, S), [], "hot", compaction="manual", ended=[CF], usage=U(read=2000, write=3500),
            base=U(read=2000, write=3500), cbp=bp(2), fbp=bp(2), boundary=P("system", 0),
            effects=[effect("break", "", to=P("system", 0),
                            note="the client compacted: its body differs from message 0 on, and the break moves no tokens")]),
        sim(190, "r4", ([], SYS1, S2), ([], SYS1, S2), [], "hot", prev=(5500, 5500), cbp=bp(3), fbp=bp(3),
            boundary=P("messages", 2)),
        # r5: the same operator shortens a new output after the compaction. It is a new change: priced as new content.
        sim(250, "r5", ([], SYS1, C3), ([], SYS1, F3), [eop(CF2, saved=TT(cacheWrite5m=7000),
            note="new content after the compaction, would have been written")], "hot", add=[op2], prev=(6000, 6000),
            cbp=bp(4), fbp=bp(4), boundary=P("messages", 3)),
    ]
    write(case("client-compaction/ends-replay-then-new-change/hot/5m",
               "One cmdfilter change is reported on request 1 and applied again on request 2 (+$0.02625 as new "
               "content, +$0.00210 as a read). On request 3 the client compacts: a summary replaces message 0, so the "
               "provider reads only the system block and the client body would have done the same. The change has ended "
               "(design §4.3): it has no operation on request 3 or later, and nothing is replayed across the "
               "compaction. The break has an empty ChangeID and moves no tokens, because B* is the system block. On "
               "request 5 the same operator shortens a new output: a new change, priced in full as a write.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-compaction-ends-replay")


# The client drops messages from the middle of its body and keeps message 0. This is an edit before the change, so
# the change ends (§4.3) just as it does when message 0 is replaced. The shape matters because the end rule compares
# positions, and a trim that keeps the head must not leave the old change running.
def c_client_trim_keeps_head():
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    C2, F2 = C1 + [("m10", 500)], F1 + [("m10", 500)]
    # The client drops m1 to m3 (9,000 tokens) and keeps m0, the shortened output's neighbours, and the tail.
    T = [m for m in M_BASE if m[0] not in ("m1", "m2", "m3")]
    T2 = T + [("m9", 8000), ("m10", 500)]
    bp = lambda last: BP(("system", 0), ("messages", 0), ("messages", last))
    steps = [
        sim(0, "r0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown"),
        sim(10, "r1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000),
            note="new content, would have been written")], "hot", add=[cmdfilter_op()], prev=(30000, 30000),
            boundary=P("messages", 8)),
        sim(70, "r2", ([], SYS1, C2), ([], SYS1, F2), [eop(CF, saved=TT(cacheRead=7000),
            note="applied again; the client's prefix would be cached")], "hot", prev=(38000, 31000),
            boundary=P("messages", 9)),
        # r3: the client trimmed m1 to m3. It now sends 29,500 tokens. The provider reads the system block and m0
        # (2,000 + 4,000 tokens) and writes the other 23,500. The old change sits after the first difference, so it ended.
        sim(130, "r3", ([], SYS1, T2), ([], SYS1, T2), [], "hot", compaction="auto", ended=[CF], usage=U(read=6000, write=sum(t for _, t in T2) + 2000 - 6000),
            base=U(read=6000, write=sum(t for _, t in T2) + 2000 - 6000), cbp=bp(len(T2) - 1), fbp=bp(len(T2) - 1),
            boundary=P("messages", 0),
            effects=[effect("break", "", to=P("messages", 0),
                            note="the client trimmed its own body after m0: B* is m0, and the break moves no tokens")]),
    ]
    write(case("client-compaction/trim-keeps-head-ends-replay/hot/5m",
               "One cmdfilter change is reported on request 1 and applied again on request 2. On request 3 the client "
               "drops m1 to m3 and keeps m0. The first difference is after m0 and before the change, so the change "
               "ended (design §4.3): it has no operation on request 3. The provider reads the system block and m0, and "
               "the client body would have done the same, so the break moves no tokens.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-trim-keeps-head-ends-replay")


# The client rewrites an early message in place, so its body is as long as before and still holds the position of the
# change. Only the edit check can end the change here: the position has not vanished. (Design §4.3, §11 question 12.)
def c_client_rewrite_in_place_ends_replay():
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    C2, F2 = C1 + [("m10", 500)], F1 + [("m10", 500)]
    E = [("m2c", 3000) if m[0] == "m2" else m for m in C2]  # m2 rewritten, same size; the change's position still exists
    bp = lambda last: BP(("system", 0), ("messages", 1), ("messages", last))
    total = SYS1[0][1] + tokens(E)
    steps = [
        sim(0, "r0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown"),
        sim(10, "r1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000),
            note="new content, would have been written")], "hot", add=[cmdfilter_op()], prev=(30000, 30000),
            boundary=P("messages", 8)),
        sim(70, "r2", ([], SYS1, C2), ([], SYS1, F2), [eop(CF, saved=TT(cacheRead=7000),
            note="applied again; the client's prefix would be cached")], "hot", prev=(38000, 31000),
            boundary=P("messages", 9)),
        # r3: the client rewrote m2 and nothing else. The provider reads the system block, m0 and m1 (2,000 + 4,000 + 3,000
        # tokens) and writes the rest. The body is as long as before, so the change's position still exists in it: the
        # change ended because the client edited before it, not because its position vanished.
        sim(130, "r3", ([], SYS1, E), ([], SYS1, E), [], "hot", usage=U(read=9000, write=total - 9000),
            base=U(read=9000, write=total - 9000), cbp=bp(len(E) - 1), fbp=bp(len(E) - 1), boundary=P("messages", 1),
            effects=[effect("break", "", to=P("messages", 1),
                            note="the client rewrote m2 itself: B* is m1, and the break moves no tokens")]),
    ]
    write(case("client-edit/rewrite-in-place-ends-replay/hot/5m",
               "One cmdfilter change is reported on request 1 and applied again on request 2. On request 3 the client "
               "rewrites m2, an early message, and keeps the body as long as before, so the position of the change "
               "still exists in it. The change ended because the client edited before it (design §4.3). It has no "
               "operation on request 3. The provider reads the system block, m0 and m1, and the client body would have "
               "done the same, so the break moves no tokens. Two rules end the change here: the client edit, and the "
               "restore check (the forwarded item has the client item's size again, because the operator reported "
               "nothing on request 3). A host that ends a change forwards the client's item, so a valid request "
               "triggers both, and this case cannot tell them apart.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-edit-rewrite-in-place-ends-replay")


# The host reports a compaction on a thread that has no change at all. The record exists and lists nothing.
def c_client_compaction_before_any_change():
    C1 = M_BASE + [("m9", 500)]
    T = [("sum", 1500), ("m9", 500), ("m10", 500)]
    bp = lambda last: BP(("system", 0), ("messages", last))
    steps = [
        sim(0, "r0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown"),
        sim(10, "r1", ([], SYS1, C1), ([], SYS1, C1), [], "hot", prev=(30000, 30000), boundary=P("messages", 8)),
        # r2: the client replaces its head with a summary. No change ran in this thread, so nothing ends.
        sim(70, "r2", ([], SYS1, T), ([], SYS1, T), [], "hot", compaction="manual", ended=[],
            usage=U(read=2000, write=tokens(*([], [], T))), base=U(read=2000, write=tokens(*([], [], T))),
            cbp=bp(2), fbp=bp(2), boundary=P("system", 0),
            effects=[effect("break", "", to=P("system", 0),
                            note="the client compacted its head: the cache has no entry before it, and the break moves no tokens")]),
    ]
    write(case("client-compaction/before-any-change/hot/5m",
               "The client compacts its body on a thread where no operator reported a change. The host reports a "
               "manual compaction. The library returns the record with an empty list of ended changes, and "
               "prices nothing.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-compaction-before-any-change")


def _cmd_head_steps(bp9=False):
    """Requests 0 to 2 of the compaction cases: a first request, one cmdfilter change, and its replay."""
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    C2, F2 = C1 + [("m10", 500)], F1 + [("m10", 500)]
    steps = [
        sim(0, "r0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown"),
        sim(10, "r1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000),
            note="new content, would have been written")], "hot", add=[cmdfilter_op()], prev=(30000, 30000),
            boundary=P("messages", 8)),
        sim(70, "r2", ([], SYS1, C2), ([], SYS1, F2), [eop(CF, saved=TT(cacheRead=7000),
            note="applied again; the client's prefix would be cached")], "hot", prev=(38000, 31000),
            boundary=P("messages", 9),
            **({"cbp": BP(("system", 0), ("messages", 9), ("messages", 10)),
                "fbp": BP(("system", 0), ("messages", 9), ("messages", 10))} if bp9 else {})),
    ]
    return steps, C2, F2


# The client edits its body after the change (the tail). The change is not affected: nothing before it moved, so it
# is replayed again. This is the negative control for the end rule: an edit at the tail must not end a change.
def c_client_tail_edit_keeps_replay():
    steps, C2, F2 = _cmd_head_steps(bp9=True)
    swap = lambda b: [("m10x", 500) if m[0] == "m10" else m for m in b]
    C3, F3 = swap(C2), swap(F2)
    bp = lambda last: BP(("system", 0), ("messages", 9), ("messages", last))
    steps.append(
        # r3: the client rewrote m10, after the change. The provider reads up to the changed message (31,000 tokens)
        # and writes m10 (500). The client body would have read 38,000 tokens up to m9.
        sim(130, "r3", ([], SYS1, C3), ([], SYS1, F3), [eop(CF, saved=TT(cacheRead=7000),
            note="applied again; the client edit is after the change, so the change goes on")], "hot",
            compaction="auto", ended=[],
            usage=U(read=31000, write=500), base=U(read=38000, write=500), cbp=bp(10), fbp=bp(10),
            boundary=P("messages", 10),
            effects=[effect("break", "", to=P("messages", 10),
                            note="the client edit is at m10, after the change: B* is m10, and the break moves no tokens")]))
    write(case("client-compaction/tail-edit-keeps-replay/hot/5m",
               "One cmdfilter change is reported on request 1 and applied again on request 2. On request 3 the client "
               "rewrites m10, a message after the change. Nothing at or before the change moved, so the change did "
               "not end (design §4.3) and it is priced as a read again (+$0.00210). The host reports the edit as an "
               "auto compaction: the record exists and its list of ended changes is empty. An edit at the tail must "
               "not end a change.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-compaction-tail-edit-keeps-replay")


# The client compacts the tail: a summary replaces the changed message and the one after it, and everything before
# stays. The change's own position now holds other content, so the change ended.
def c_client_tail_compaction_ends_replay():
    steps, C2, F2 = _cmd_head_steps()
    T = M_BASE + [("sumT", 1500)]
    bp = lambda last: BP(("system", 0), ("messages", last))
    steps.append(
        # r3: the cache holds entries at the system block and at m10 only, so the provider reads the system block
        # (2,000 tokens) and writes the other 29,500.
        sim(130, "r3", ([], SYS1, T), ([], SYS1, T), [], "hot", compaction="unknown", ended=[CF], usage=U(read=2000, write=29500),
            base=U(read=2000, write=29500), cbp=bp(9), fbp=bp(9), boundary=P("system", 0),
            effects=[effect("break", "", to=P("system", 0),
                            note="the client compacted its tail: the cache has no entry before it, and the break moves no tokens")]))
    write(case("client-compaction/tail-compaction-ends-replay/hot/5m",
               "One cmdfilter change is reported on request 1 and applied again on request 2. On request 3 the client "
               "replaces m9 and m10 with a summary and keeps m0 to m8. The change's position holds other content, so "
               "the change ended (design §4.3): no operation on request 3. The cache holds entries only at the system "
               "block and at m10, so the provider reads the system block and writes the rest. The client body would have "
               "done the same, so the break moves no tokens.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-compaction-tail-ends-replay")


# Two compactions in one thread. Each ends the change that was running, and the change after it is priced as new
# content. The first compaction replaces the head; the second drops a message from the middle of the new body.
def c_client_two_compactions():
    steps, C2, F2 = _cmd_head_steps()
    CF2, CF3 = "cg:cmdfilter:9b17", "cg:cmdfilter:5e02"
    S = [("sum", 1500), ("m9k", 1500), ("m10k", 500)]
    S2 = S + [("m11", 500)]
    C3, F3 = S2 + [("m12", 8000)], S2 + [("m12f", 1000)]
    C4, F4 = C3 + [("m13", 500)], F3 + [("m13", 500)]
    # The second compaction drops m9k from the middle. The client body keeps m12 at its full size.
    T = [S2[0], S2[2], S2[3], ("m12", 8000), ("m13", 500)]
    C6, F6 = T + [("m14", 8000)], T + [("m14f", 1000)]
    bp = lambda last: BP(("system", 0), ("messages", last))
    op2 = rewrite("o2", "context-guru.cmdfilter", "messages", 4, 8000, 1000, CF2)
    op3 = rewrite("o3", "context-guru.cmdfilter", "messages", 5, 8000, 1000, CF3)
    steps += [
        # r3: first compaction (head). The old change ended.
        sim(130, "r3", ([], SYS1, S), ([], SYS1, S), [], "hot", compaction="manual", ended=[CF], usage=U(read=2000, write=3500),
            base=U(read=2000, write=3500), cbp=bp(2), fbp=bp(2), boundary=P("system", 0),
            effects=[effect("break", "", to=P("system", 0),
                            note="the first compaction: its body differs from message 0 on, and the break moves no tokens")]),
        sim(190, "r4", ([], SYS1, S2), ([], SYS1, S2), [], "hot", prev=(5500, 5500), cbp=bp(3), fbp=bp(3),
            boundary=P("messages", 2)),
        # r5: a new change after the first compaction, priced as new content.
        sim(250, "r5", ([], SYS1, C3), ([], SYS1, F3), [eop(CF2, saved=TT(cacheWrite5m=7000),
            note="new content after the first compaction, would have been written")], "hot", add=[op2],
            prev=(6000, 6000), cbp=bp(4), fbp=bp(4), boundary=P("messages", 3)),
        # r6: the second change is replayed.
        sim(310, "r6", ([], SYS1, C4), ([], SYS1, F4), [eop(CF2, saved=TT(cacheRead=7000),
            note="applied again; the client's prefix would be cached")], "hot", prev=(14000, 7000),
            cbp=bp(5), fbp=bp(5), boundary=P("messages", 4)),
        # r7: second compaction (middle): m9k is dropped. The provider reads the system block only (2,000 tokens) and writes the rest.
        sim(370, "r7", ([], SYS1, T), ([], SYS1, T), [], "hot", compaction="auto", ended=[CF2], usage=U(read=2000, write=11000),
            base=U(read=2000, write=11000), cbp=bp(4), fbp=bp(4), boundary=P("system", 0),
            effects=[effect("break", "", to=P("system", 0),
                            note="the second compaction: the cache has no entry before it, and the break moves no tokens")]),
        # r8: a third change after the second compaction, priced as new content.
        sim(430, "r8", ([], SYS1, C6), ([], SYS1, F6), [eop(CF3, saved=TT(cacheWrite5m=7000),
            note="new content after the second compaction, would have been written")], "hot", add=[op3],
            prev=(13000, 13000), cbp=bp(5), fbp=bp(5), boundary=P("messages", 4)),
    ]
    write(case("client-compaction/two-compactions-each-ends-replay/hot/5m",
               "Three cmdfilter changes in one thread, separated by two client compactions. The first change is "
               "reported and replayed, then the client replaces message 0 with a summary: the change ended and a second "
               "change after it is priced as new content (+$0.02625), then replayed (+$0.00210). Then the client drops "
               "m9k from the middle of its body: the second change ended too, and a third change is priced as new "
               "content again. Each compaction ends only the change that was running, and no credit crosses either one.",
               ISSUES, DIMS("rewrite", "afterBStarToL", "hit", **{"break": "client"}), steps),
          "client-compaction-two-compactions")


# accountant#16: the clock and the order of calls do not win over the usage.
def c_clock():
    M = M_BASE
    T = 30000  # tokens of SYS1 + M_BASE
    r0 = sim(0, "r0", ([], SYS1, M), ([], SYS1, M), [], "unknown")
    c1 = M + [("m9", 500)]
    def edge(sec, rid, phase, read):
        u = U(read=T, write=500) if read else U(write=T + 500)
        return sim(sec, rid, ([], SYS1, c1), ([], SYS1, c1), [], phase, prev=(T, T), usage=u,
                   base=u,
                   boundary=P("messages", 8) if phase == "hot" else None)
    write_issue("clock/ttl-edge-read-covers-extent/hot", 16,
                "A request arrives 2 seconds past the 5m TTL, but the usage bills a read of all 30,000 tokens of the "
                "previous extent. The provider held the entry (clock skew, queue time), so the phase is hot, not cold. "
                "A billed read beats the clock (design 5.2).",
                DIMS("none", "afterBStar", "hit"), [r0, edge(302, "r1", "hot", True)])
    write_issue("clock/ttl-edge-past-margin/cold", 16,
                "The same request, 11 seconds past the TTL, bills no read: all 30,500 tokens are written. With no read "
                "there is no evidence against the clock, so the phase stays cold.",
                DIMS("none", "afterBStar", "miss"), [r0, edge(311, "r1", "cold", False)])
    b = M + [("b1", 500)]
    c = b + [("c", 500)]
    d = b + [("d", 500)]
    write_issue("clock/out-of-order-at/keeps-newer-fingerprint", 16,
                "Request C has an At one hour before B and is accepted. It is unknown (the clock says nothing) and does "
                "not replace B's fingerprint. Request D, 1 minute after B, is compared with B: hot.",
                DIMS("none", "afterBStar", "hit"),
                [r0, sim(120, "B", ([], SYS1, b), ([], SYS1, b), [], "hot", prev=(T, T), boundary=P("messages", 8)),
                 sim(-3480, "C", ([], SYS1, c), ([], SYS1, c), [], "unknown"),
                 sim(180, "D", ([], SYS1, d), ([], SYS1, d), [], "hot", prev=(T + 500, T + 500), boundary=P("messages", 9))])
    sub = {"id": "a:sub", "parent": "main"}
    fk = M + [("x0", 500)]
    write_issue("clock/fork-seeded-from-parent/hot", 16,
                "A subagent forks from a warm main thread and keeps its whole cached prefix. It reads 30,000 tokens. The "
                "library seeds the fork from the parent's fingerprint (design 5.2), so the phase is hot at messages[8] "
                "with no penalty_unknown.",
                DIMS("none", "afterBStar", "fork", threads="2"),
                [r0, sim(30, "fork-0", ([], SYS1, fk), ([], SYS1, fk), [], "hot", prev=(T, T), boundary=P("messages", 8),
                         thread=sub)])
    write_issue("clock/fork-past-parent-ttl/unseeded-read", 16,
                "A fork starts 20 minutes after its parent's last request, and the usage shows a read of 30,000 tokens. "
                "The library does not seed a fork past the TTL, since a cold seed would raise the saving. The phase is "
                "unknown, and the read makes it hot with penalty_unknown.",
                DIMS("none", "afterBStar", "fork", threads="2"),
                [r0, sim(1200, "fork-0", ([], SYS1, fk), ([], SYS1, fk), [], "hot", prev=(T, T), boundary=P("messages", 9),
                         thread=sub, unpriced=[{"reason": "penalty_unknown"}])])


# accountant#40 and #41: the clock judges the entry the previous request WROTE, breakpoint by breakpoint.
# Every case: r0 has messages a=2000, b=3000, c=2000 tokens; change cg:ttl:c1 shrinks b to b2=1000 (-2000 tokens).
# r1 adds d=500 and replays the change: its saving is 2000 tokens. Hot: read tier. Cold: write tier.
def c_ttl():
    CH = "cg:ttl:c1"
    cm = [("a", 2000), ("b", 3000), ("c", 2000)]
    fm = [("a", 2000), ("b2", 1000), ("c", 2000)]
    c1m, f1m = cm + [("d", 500)], fm + [("d", 500)]
    op = rewrite("o1", "mask", "messages", 1, 3000, 1000, CH)

    def bpl(*pairs):
        return [{"pos": P("messages", i), "ttl": t} for i, t in pairs]

    def facts(ttl):
        return {"ttl": ttl, "lifetime": "exact"}

    def first(b0, ttl, w1h, saved, note):
        # r0: unknown phase. The forwarded body is 5000 tokens, all written. The client body would write 7000.
        # Of the saved 2000 tokens, the ones under a 1h mark are written at 1h.
        return step(0, "r0", body([], [], cm), body([], [], fm), b0, b0, [op], U(write=5000, w1h=w1h),
                    expect("unknown", U(write=7000, w1h=w1h + 2000 if saved == "1h" else w1h),
                           [eop(CH, saved=TT(cacheWrite1h=2000) if saved == "1h" else TT(cacheWrite5m=2000), note=note)],
                           unpriced=[{"reason": "penalty_unknown"}]),
                    facts=facts(ttl))

    # 1. #40: mixed marks, a 10 minute gap, a billed read of the 1h head.
    mark0 = bpl((1, "1h"), (2, "5m"))
    mark1 = bpl((1, "1h"), (3, "5m"))
    s0 = step(0, "r0", body([], [], cm), body([], [], fm), mark0, mark0, [op], U(write=5000, w1h=3000),
              expect("unknown", U(write=7000, w1h=5000),
                     [eop(CH, saved=TT(cacheWrite1h=2000), note="the shrunk part is under the 1h mark")],
                     unpriced=[{"reason": "penalty_unknown"}]), facts=facts("5m"))
    s1 = step(600, "r1", body([], [], c1m), body([], [], f1m), mark1, mark1, [], U(read=3000, write=2500),
              expect("hot", U(read=5000, write=2500),
                     [eop(CH, saved=TT(cacheRead=2000), note="the 1h head is alive at minute 10: 2000 tokens are read")],
                     boundary=P("messages", 1)),
              facts=facts("5m"))
    write_issue("ttl/mixed-1h-head-5m-tail/read-after-10-minutes", 40,
                "r0 sends a 1h mark on messages[1] and a 5m mark on messages[2]. The change shrinks messages[1] from 3,000 "
                "to 1,000 tokens. The forwarded head is 2,000 + 1,000 = 3,000 tokens (1h), the tail 2,000 (5m): r0 writes "
                "5,000, of which 3,000 at 1h. r1 comes 10 minutes later with marks on messages[1] (1h) and messages[3] "
                "(5m). The provider reads the 3,000-token 1h head and writes the 2,000 tail and the 500 new message again: "
                "read 3,000, write 2,500. The 5m tail expired, the 1h head did not. The host reports Facts.TTL 5m. The "
                "replayed 2,000 tokens are inside the head, so they are READ (2,000 x $0.30/M), not written. Before the fix "
                "the phase was cold and the saving a 1h write (2,000 x $6.00/M).",
                DIMS("rewrite", "afterBStarToL", "hit", ttl="mixed"), [s0, s1])

    # 2. #41: a 5m entry, re-asked at 1h after 30 minutes.
    m5 = bpl((2, "5m"))
    m1h = bpl((3, "1h"))
    s0 = first(m5, "5m", 0, "5m", "first request: nothing is cached")
    s1 = step(1800, "r1", body([], [], c1m), body([], [], f1m), m1h, m1h, [], U(write=5500, w1h=5500),
              expect("cold", U(write=7500, w1h=7500),
                     [eop(CH, saved=TT(cacheWrite1h=2000), note="the 5m entry expired at minute 5: 2000 written at 1h")]),
              facts=facts("1h"))
    write_issue("ttl/5m-entry-asked-at-1h/cold-after-30-minutes", 41,
                "r0 writes a 5m entry. r1 comes 30 minutes later and asks for a 1h mark (Facts.TTL 1h). The entry was "
                "written for 5m and is long expired, so the phase is cold: r1 writes all 5,500 tokens at 1h. The replayed "
                "2,000 tokens are written again at the tier of r1's mark: 2,000 x $6.00/M. Before the fix the library used "
                "r1's TTL (1h), saw 30 minutes inside it, and said hot with a 2,000-token read.",
                DIMS("rewrite", "afterBStarToL", "miss", ttl="5m-then-1h"), [s0, s1])

    # 3. #41: a 1h entry, a 10 minute gap, a request with 5m marks.
    m1h0 = bpl((2, "1h"))
    m5b = bpl((3, "5m"))
    s0 = first(m1h0, "1h", 5000, "1h", "first request: nothing is cached")
    s1 = step(600, "r1", body([], [], c1m), body([], [], f1m), m5b, m5b, [], U(read=5000, write=500),
              expect("hot", U(read=7000, write=500),
                     [eop(CH, saved=TT(cacheRead=2000), note="the 1h entry is alive at minute 10: 2000 tokens are read")],
                     boundary=P("messages", 2)),
              facts=facts("5m"))
    write_issue("ttl/1h-entry-asked-at-5m/hot-after-10-minutes", 41,
                "r0 writes a 1h entry (5,000 tokens). r1 comes 10 minutes later with a 5m mark (Facts.TTL 5m) and the "
                "provider reads the whole entry: read 5,000, write 500. The entry was written for 1h, so it is alive. The "
                "replayed 2,000 tokens are read: 2,000 x $0.30/M. Before the fix the library used r1's TTL (5m), said cold "
                "and booked a 5m write (2,000 x $3.75/M), 12.5 times too much.",
                DIMS("rewrite", "afterBStarToL", "hit", ttl="1h-then-5m"), [s0, s1])

    # 4. #41 related: a 5m entry, a 5m30s gap, a billed read of the whole entry.
    s0 = first(m5, "5m", 0, "5m", "first request: nothing is cached")
    s1 = step(330, "r1", body([], [], c1m), body([], [], f1m), bpl((3, "5m")), bpl((3, "5m")), [], U(read=5000, write=500),
              expect("hot", U(read=7000, write=500),
                     [eop(CH, saved=TT(cacheRead=2000), note="the provider read the entry: the read beats the clock")],
                     boundary=P("messages", 2)),
              facts=facts("5m"))
    write_issue("clock/read-beats-clock-past-ttl/hot", 41,
                "r0 writes a 5m entry (5,000 tokens). r1 comes 5 minutes 30 seconds later, 30 seconds past the TTL, "
                "and the provider billed a read of the whole entry: read 5,000, write 500. The provider measures the TTL "
                "from the end of the previous response, so a long streamed response can put the entry inside the TTL. "
                "The billed read is the evidence, and it beats the clock at any gap: the phase is hot and the replayed "
                "2,000 tokens are read (2,000 x $0.30/M). Before the fix the margin was 10 seconds and the phase was cold.",
                DIMS("rewrite", "afterBStarToL", "hit"), [s0, s1])
    # 5. Review round 1: the 1h head is alive, the 5m tail is dead on the clock, and the read covers the tail.
    # r0: a=2000, b=3000, c=2000 -> c shrinks to c2=500. Forwarded 5500 (5000 at 1h). Client would write 7000.
    c2m = [("a", 2000), ("b", 3000), ("c2", 500)]
    mk0 = bpl((1, "1h"), (2, "5m"))
    mk1 = bpl((1, "1h"), (3, "5m"))
    op2 = rewrite("o1", "mask", "messages", 2, 2000, 500, CH)
    s0 = step(0, "r0", body([], [], cm), body([], [], c2m), mk0, mk0, [op2], U(write=5500, w1h=5000),
              expect("unknown", U(write=7000, w1h=5000),
                     [eop(CH, saved=TT(cacheWrite5m=1500), note="first request: nothing is cached")],
                     unpriced=[{"reason": "penalty_unknown"}]), facts=facts("5m"))
    s1 = step(360, "r1", body([], [], c1m), body([], [], c2m + [("d", 500)]), mk1, mk1, [], U(read=5500, write=500),
              expect("hot", U(read=7000, write=500),
                     [eop(CH, saved=TT(cacheRead=1500), note="the tail is dead on the clock but was billed as a read")],
                     boundary=P("messages", 2)), facts=facts("5m"))
    write_issue("ttl/mixed-dead-tail-read/hot-after-6-minutes", 40,
                "r0 sends a 1h mark on messages[1] and a 5m mark on messages[2]. The change shrinks messages[2] from 2,000 "
                "to 500 tokens, so the forwarded body is 5,500 tokens and r0 writes 5,500, of which 5,000 at 1h. r1 comes "
                "6 minutes later: the 1h head is alive, the 5m tail is dead on the clock. The provider billed a read of "
                "all 5,500 tokens. The billed read proves the tail was alive, so the boundary is the end of messages[2] "
                "and the replayed 1,500 tokens are READ (1,500 x $0.30/M = $0.00045). Without the read rule the library "
                "stopped at the head and booked a 5m write (1,500 x $3.75/M).",
                DIMS("rewrite", "afterBStarToL", "hit", ttl="mixed"), [s0, s1])

    # 6. Review round 1: a fork of a mixed entry, 10 minutes later. The 1h head is alive, so the fork is seeded.
    sub = {"id": "a:sub", "parent": "main"}
    s0 = step(0, "r0", body([], [], cm), body([], [], cm), mark0, mark0, [], U(write=7000, w1h=5000),
              expect("unknown", U(write=7000, w1h=5000), [], unpriced=[{"reason": "penalty_unknown"}]), facts=facts("5m"))
    s1 = step(600, "fork-0", body([], [], c1m), body([], [], c1m), mark1, mark1, [], U(read=5000, write=2500),
              expect("hot", U(read=5000, write=2500), [], boundary=P("messages", 1)), facts=facts("5m"), thread=sub)
    write_issue("ttl/mixed-fork/seeded-while-head-alive", 40,
                "The main thread writes a mixed entry: a 1h mark on messages[1] (a+b = 5,000 tokens) and a 5m mark on "
                "messages[2] (7,000 tokens in all). A subagent forks 10 minutes later and keeps the whole prefix. The "
                "5m tail is dead, the 1h head is alive, so the library seeds the fork from the parent. The phase is hot "
                "at messages[1], with no penalty_unknown. The provider read the 5,000-token head and wrote 2,500.",
                DIMS("none", "afterBStar", "fork", threads="2", ttl="mixed"), [s0, s1])


# Three cases that mix sessions, threads and models.
def c_mix():
    # 1. A fork: the first request of a thread forked from main sends main's conversation with a summary.
    tools, sysb, old = summary_bodies()
    FK = "cg:summarize:f0a1"
    fork = {"id": "p:fork", "parent": "main"}
    summary, tail = [("summary", 500)], old[3:]
    call = {"model": "summarizer-model", "params": {"effort": "low"},
            "usage": {"input": 10000, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": 500, "costUSD": 0.0125}}
    spend = {"id": "op1", "kind": "spend", "operator": "context-guru.summarize", "changeId": FK, "call": call}
    rw = rewrite("op2", "context-guru.summarize", "messages", 0, 10000, 500, FK, splice={"at": 0, "removed": 3, "inserted": 1})
    s0 = sim(0, "main-0", (tools, sysb, old), (tools, sysb, old), [], "unknown")
    # The provider caches by content, so the fork reads main's tools and system (8,000) and writes the rest.
    s1 = sim(60, "fork-0", (tools, sysb, old + [("n0", 500)]), (tools, sysb, summary + tail + [("n0", 500)]),
             [eop(FK, call=call), eop(FK, saved=TT(cacheWrite5m=9500))], "hot", add=[spend, rw],
             usage=U(read=8000, write=4000), base=U(read=8000, write=13500),
             unpriced=[{"reason": "penalty_unknown"}], boundary=P("system", 0), thread=fork)
    write_issue("threads/fork-first-request/summary", 423,
                "A thread forked from main sends main's conversation with a summary on its first request. Main cached "
                "21,000 tokens; the provider caches by content, so the fork reads main's tools and system (8,000) and writes "
                "the rest. The fork's body does not keep main's cached extent (the summary replaces its first messages), so "
                "the library does not seed it from Thread.Parent (design 5.2): the phase is unknown and the read in the "
                "usage makes it hot up to the end of system: the 9,500 removed tokens come "
                "after B* and are a write, +$0.03563, less the summarizer call $0.0125. cg prices a summarizer's first "
                "removal as a read whenever the request hit the cache (uniqueRate), +$0.00285. Main's entry held the whole "
                "conversation and the provider caches by content, so without the summary the fork would have read those "
                "tokens: cg's tier is nearer the truth here, and the library cannot see it because the fork's body "
                "differs from the parent's inside the cached extent.",
                DIMS("rewrite+spend", "afterBStar", "fork", threads="2"), [s0, s1],
                open_=["a fork that rewrites inside its parent's cached extent is priced as a thread with no history: the "
                       "library seeds a fork from Thread.Parent only when the fork keeps the parent's whole extent"])

    # 2. A subagent already runs on the small model. Main moves to it with nothing reported: the small
    # model has no entry for main's prefix, so nothing is read. The break says why.
    sub = {"id": "a:sub", "parent": "main"}
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    m0 = sim(0, "main-0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown", model="agent-model")
    s0 = sim(5, "sub-0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown", thread=sub, model="small-model")
    m1 = sim(10, "main-1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000))], "hot",
             add=[cmdfilter_op()], prev=(30000, 30000), boundary=P("messages", 8), model="small-model",
             usage=U(write=31000), base=U(write=38000),
             effects=[effect("break", "", saved=TT(cacheRead=30000), caused=TT(cacheWrite5m=30000), to=P("messages", 8),
                             cause="model_changed", note="the small model holds no entry for main's prefix")])
    s1 = sim(15, "sub-1", ([], SYS1, M_BASE + [("x0", 500)]), ([], SYS1, M_BASE + [("x0", 500)]), [], "hot",
             prev=(30000, 30000), thread=sub, model="small-model")
    write_issue("threads/model-switch-unreported/main-joins-subagent-model", 423,
                "Main runs on the large model and a subagent on the small one, each with its own cache entry. Main "
                "moves to the small model and nothing reports it: the request names a different model from main's "
                "previous one, and the provider keeps a cache for each model, so main reads nothing and writes all "
                "31,000 tokens. The library sees the loss from the usage and records a break nobody caused, 30,000 "
                "tokens from read to write, labelled model_changed. It is in the client body's bill too, so the baseline "
                "equals the bill and the break nets out. The cmdfilter saving after B* is a write, 7,000 x $1.00. The "
                "subagent's own thread is untouched. cg's seenModel is keyed by model alone, so main's first request "
                "on the small model is not a cold start: the subagent used it first.",
                DIMS("none", "beforeBStar", "break", threads="2", models="2", **{"break": "model"}), [m0, s0, m1, s1])

    # 2b. The same id in two spellings is not a model change (accountant#44). Main moves from a model id to
    # its dated spelling, and the host gives both the same ModelKey. The entry is lost all the same, so the
    # break is there, with no cause.
    k0 = sim(0, "main-0", ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown", model="small-model", model_key="small")
    k1 = sim(10, "main-1", ([], SYS1, C1), ([], SYS1, F1), [eop(CF, saved=TT(cacheWrite5m=7000))], "hot",
             add=[cmdfilter_op()], prev=(30000, 30000), boundary=P("messages", 8), model="small-model-20251001",
             model_key="small", usage=U(write=31000), base=U(write=38000),
             effects=[effect("break", "", saved=TT(cacheRead=30000), caused=TT(cacheWrite5m=30000), to=P("messages", 8),
                             note="the entry was lost; both ids are the model small, so no cause")])
    write_issue("threads/model-switch-unreported/same-model-key-two-spellings", 44,
                "Main sends a request on small-model and the next one on small-model-20251001. The host gives both ids "
                "the ModelKey small, so they are one model. The usage shows the entry lost: nothing is read and all "
                "31,000 tokens are written. The library records the break nobody caused, 30,000 tokens from read to "
                "write, with no cause: the break is not labelled model_changed, because the model did not change. "
                "It is in the client body's bill too, so the baseline equals the bill and the break nets out.",
                DIMS("none", "beforeBStar", "break", threads="1", models="1", **{"break": "unexplained"}), [k0, k1])

    # 3. Two sessions, each with a main and a subagent, all reporting the same ChangeID.
    C1, F1 = M_BASE + [("m9", 8000)], M_BASE + [("m9f", 1000)]
    first = lambda sec, rid, ses, th: sim(sec, rid, ([], SYS1, M_BASE), ([], SYS1, M_BASE), [], "unknown", session=ses, thread=th)
    newc = lambda sec, rid, ses, th: sim(sec, rid, ([], SYS1, C1), ([], SYS1, F1),
                                         [eop(CF, saved=TT(cacheWrite5m=7000))], "hot", add=[cmdfilter_op()],
                                         prev=(30000, 30000), boundary=P("messages", 8), session=ses, thread=th)
    sa, sb = "s-a", "s-b"
    sub_a, sub_b = {"id": "a:sub", "parent": "main"}, {"id": "a:sub", "parent": "main"}
    steps = [first(0, "a-main-0", sa, None), newc(10, "a-main-1", sa, None),
             first(20, "a-sub-0", sa, sub_a), newc(30, "a-sub-1", sa, sub_a),
             first(40, "b-main-0", sb, None), newc(50, "b-main-1", sb, None),
             first(60, "b-sub-0", sb, sub_b), newc(70, "b-sub-1", sb, sub_b)]
    write_issue("threads/two-sessions-same-change/main-and-subagent", 175,
                "Two sessions each have a main thread and a subagent called a:sub. All four report the same cmdfilter "
                "ChangeID on their own request, as the operator derives the same one for the same output. Each prices it "
                "once, as new on its own thread, +$0.02625, with no unique_change failure. ThreadKey is session plus "
                "thread, so neither the other session's main nor its a:sub reaches it. cg's MarkUnique keys by tenant, "
                "component and content, so only the first of the four is unique to it.",
                DIMS("rewrite", "afterBStarToL", "hit", sessions="2", threads="4"), steps)


# Cases 14 (#45) and 15 (#567): breakpoint operators that report success.
SPL = "cg:cachesplit:e4d1"


def c14():
    msgs0 = [("m0", 5000)]
    msgs1 = msgs0 + [("m1", 1000)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3300, 3300, SPL)
    for cid, delivered in (("split/reported-not-delivered", False), ("split/delivered/stable-half-read", True)):
        fb_idx = 0 if delivered else 1
        s0 = sim(0, "r0", ([], [("sysblk", 3300)], msgs0), ([], [("sysstable", 3000), ("sysvol", 300)], msgs0), [],
                 "unknown", add=[op], cbp=BP(("system", 0), ("messages", 0)), fbp=BP(("system", fb_idx), ("messages", 0)))
        brk = effect("break", "", note="the client changed its own system block: B* is before it, so the range is "
                     "empty (To nil) and the break moves no tokens (§4.4, §5.1, §6.2)")
        if delivered:
            usage = U(read=3000, write=6300)
            effects = [effect("split", SPL, saved=TT(cacheWrite5m=3000), caused=TT(cacheRead=3000), to=P("system", 0),
                              note="the stable half is read, not written"), brk]
            ops = []
        else:
            usage = U(write=9300)
            effects = [brk]
            ops = []
        # cachesplit runs on every request, so it reports again; the client edit of system[0] ended the active one
        s1 = sim(40, "r1", ([], [("sysblk2", 3300)], msgs1), ([], [("sysstable", 3000), ("sysvol2", 300)], msgs1), ops,
                 "hot", prev=(8300, 8300), usage=usage, base=U(write=9300), add=[op],
                 cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", fb_idx), ("messages", 1)),
                 effects=effects)
        if delivered:
            desc = ("On Anthropic the breakpoint follows the stable half: cachesplit moves it there. The client changes "
                    "the volatile 300 tokens, so the client body would write all 9,300 tokens, and the forwarded body "
                    "reads the stable 3,000 and writes 6,300. A split effect credits 3,000 x ($3.75 - $0.30) = +$0.01035. "
                    "The contrast case of reported-not-delivered.")
        else:
            desc = ("On Bedrock Converse the cachePoint is its own entry after the block, and the split put the volatile "
                    "half before it, so the breakpoint still covers the churn. cachesplit reported a rewrite of equal "
                    "size and shows as active. The volatile text changes: the forwarded body reads nothing, like the "
                    "client body. The library prices from usage and breakpoints: no split effect, no saving, and the "
                    "operator is credited nothing.")
        write_issue(cid, 45, desc, DIMS("setting-breakpoint", "system", "split" if delivered else "no-effect"),
                    [s0, s1],
                    open_=["design §5.2 rows 1 and 2: the client changed its own system block, and the split moved that "
                           "change one block later in forwarded space. Which row names the break (and so whether the "
                           "empty-ChangeID break is added beside the split effect) is not stated"])


# context-guru#909: a split on a short conversation. The volatile tail is a large part of the entry, so the
# stable half the provider read is within the read tolerance (128 tokens plus 2%) of the whole previous entry.
# The B* rule then took the read as proof that nothing broke and set B* to the end of the extent. The split
# effect needs B* before the changed system block, so it was lost.
def s909():
    SPL9 = "cg:cachesplit:s909"
    m0 = [("m0", 20)]
    m1 = m0 + [("m1", 50)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3150, 3150, SPL9)
    s0 = sim(0, "r0", ([], [("sysblk", 3150)], m0), ([], [("sysstable", 3000), ("sysvol", 150)], m0), [],
             "unknown", add=[op], cbp=BP(("system", 0), ("messages", 0)), fbp=BP(("system", 0), ("messages", 0)))
    brk = effect("break", "", note="the client changed its own system block: B* is before it, so the range is "
                 "empty (To nil) and the break moves no tokens")
    split = effect("split", SPL9, saved=TT(cacheWrite5m=3000), caused=TT(cacheRead=3000), to=P("system", 0),
                   note="the stable half is read, not written")
    s1 = sim(40, "r1", ([], [("sysblk2", 3150)], m1), ([], [("sysstable", 3000), ("sysvol2", 150)], m1), [],
             "hot", prev=(3170, 3170), usage=U(read=3000, write=220), base=U(write=3220), add=[op],
             cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", 0), ("messages", 1)),
             effects=[split, brk])
    write_issue("split/short-conversation/tail-changed", "context-guru-909",
               "cachesplit on a short conversation. The volatile tail is 150 tokens of a 3,170-token entry. The tail "
               "changes and the turn adds 50 tokens. The provider reads the 3,000-token stable half. That read is "
               "within the read tolerance (128 tokens plus 2%) of the whole previous entry, but it is not proof that "
               "nothing broke: the client changed its system block. B* stays before that block. The client body would "
               "write all 3,220 tokens, so the split saves 3,000 x ($3.75 - $0.30) = +$0.01035.",
               DIMS("setting-breakpoint", "system", "split"), [s0, s1],
               open_=["design §5.2 rows 1 and 2: which row names the break beside the split effect is not stated"])


def c15():
    sysb = [("s0", 200), ("s1", 800), ("s2", 1500)]
    m = [("m0", 5000), ("m1", 2000)]
    bp = lambda last: BP(("system", 1), ("system", 2), ("messages", last))
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown", cbp=bp(1), fbp=bp(1))
    m2 = m + [("m2", 1000)]
    s1 = sim(40, "r1", ([], sysb, m2), ([], sysb, m2), [], "hot", prev=(9500, 9500), cbp=bp(2), fbp=bp(2),
             boundary=P("messages", 1))
    write_issue("split/marker-not-in-system", 567,
                "On Claude Code 2.1.299 the git snapshot sits in the first user message, after both system breakpoints, "
                "and does not change during the session. cachesplit finds no marker and reports no operation. The thread "
                "reads the whole prefix on a hot turn: no operation, no effect, a zero total.",
                DIMS("none", "none", "hit"), [s0, s1])
    bp2 = lambda last: BP(("system", 2), ("messages", 0), ("messages", last))
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown", cbp=bp2(1), fbp=bp2(1))
    m3 = [("m0b", 5000), ("m1", 2000), ("m2", 1000)]
    s1 = sim(40, "r1", ([], sysb, m3), ([], sysb, m3), [], "hot", prev=(9500, 9500), cbp=bp2(2), fbp=bp2(2),
             usage=U(read=2500, write=8000), base=U(read=2500, write=8000), boundary=P("system", 2),
             effects=[effect("break", "", to=P("system", 2),
                             note="a volatile block inside the span a messages[0] breakpoint hashes: B* is the "
                                  "system breakpoint before it, so the break moves no tokens")])
    write_issue("split/volatile-in-first-message", 567,
                "The variant issue 567 could not measure: the volatile snapshot changes inside message 0, which a "
                "messages[0] breakpoint hashes. The provider reads through system (2,500 tokens) and writes the other "
                "8,000. The client made the change, so the break has an empty ChangeID and, with B* at the system "
                "breakpoint, no priced tokens. The 8,000 written tokens are the room an operator that moved that "
                "breakpoint could win back, but they are not a cost of any operation.",
                DIMS("none", "messages", "break", **{"break": "client"}), [s0, s1], open_=[BSTAR_SNAP])


# Case 16 (#481): a restore adds the original back.
MK16, RS = "cg:mask:8d11", "cg:restore:2c9e"


def c16():
    sysb = [("sys", 2000)]
    base_c = [("m0", 3000), ("m1", 5000)]
    base_f = [("m0", 3000), ("m1f", 500)]
    mask = rewrite("o1", "context-guru.mask", "messages", 1, 5000, 500, MK16)
    s0 = sim(0, "r0", ([], sysb, base_c), ([], sysb, base_f), [eop(MK16, saved=TT(cacheWrite5m=4500))], "unknown",
             add=[mask])
    c1, f1 = base_c + [("m2", 1000)], base_f + [("m2", 1000)]
    s1 = sim(40, "r1", ([], sysb, c1), ([], sysb, f1), [eop(MK16, saved=TT(cacheRead=4500))], "hot", prev=(10000, 5500),
             boundary=P("messages", 1))
    c2, f2 = c1 + [("m3", 500)], f1 + [("m3", 500), ("r1", 5000)]
    restore = rewrite("o2", "context-guru.expand_restore", "messages", 4, 0, 5000, RS,
                      splice={"at": 4, "removed": 0, "inserted": 1})
    s2 = sim(80, "r2", ([], sysb, c2), ([], sysb, f2),
             [eop(RS, caused=TT(cacheWrite5m=5000), note="the restored copy is new content: written once"),
              eop(MK16, saved=TT(cacheRead=4500))], "hot", add=[restore], prev=(11000, 6500), boundary=P("messages", 2))
    steps = [s0, s1, s2]
    pc, pf = tokens(sysb, c2), tokens(sysb, f2)
    c, f = c2, f2
    for k, t in ((3, 120), (4, 160)):
        c, f = c + [(f"n{k}", 500)], f + [(f"n{k}", 500)]
        steps.append(sim(t, f"r{k}", ([], sysb, c), ([], sysb, f),
                         [eop(MK16, saved=TT(cacheRead=4500)), eop(RS, caused=TT(cacheRead=5000))], "hot",
                         prev=(pc, pf), boundary=P("messages", len(c) - 2)))
        pc, pf = tokens(sysb, c), tokens(sysb, f)
    write_issue("rewrite/fixed-restore-cost/hot/5m", 481,
                "mask compacts a 5,000-token output to 500 (saves 4,500). On request 2 the model expands it, and "
                "fixed_restore appends the original as a new 5,000-token message while the marker stays. The restore "
                "grows the body, so it is a cost by its position: written once on request 2 (5,000 x $3.75 = -$0.01875) "
                "and read on every later hot request (-$0.00150), against the mask's +$0.00135. Each later request nets "
                "-$0.00015: the compaction's credit is never shown without the restore beside it.",
                DIMS("rewrite", "afterBStar", "hit", operations="2", restore="fixed"), steps,
                open_=[BSTAR_SNAP])

    bpx = lambda last: BP(("system", 0), ("messages", 0), ("messages", last))
    s0 = sim(0, "r0", ([], sysb, base_c), ([], sysb, base_f), [eop(MK16, saved=TT(cacheWrite5m=4500))], "unknown",
             add=[mask], cbp=bpx(1), fbp=bpx(1))
    s1 = sim(40, "r1", ([], sysb, c1), ([], sysb, f1), [eop(MK16, saved=TT(cacheRead=4500))], "hot", prev=(10000, 5500),
             cbp=bpx(2), fbp=bpx(2), boundary=P("messages", 1))
    s2 = sim(80, "r2", ([], sysb, c2), ([], sysb, c2), [], "hot", prev=(11000, 6500), usage=U(read=5000, write=6500),
             cbp=bpx(3), fbp=bpx(3), boundary=P("messages", 2),
             effects=[effect("break", "", saved=TT(cacheRead=6000), caused=TT(cacheWrite5m=6000), baseline=False,
                             frm=P("messages", 0), to=P("messages", 2),
                             note="the original goes back in place: the mask ended and the provider holds the marker")])
    write_issue("rewrite/restore-in-place/hot/5m", 481,
                "The variant with fixed_restore off: the expanded original goes back in place. The mask no longer "
                "applies, so it saves nothing, but the provider holds the marker form from the earlier requests, so "
                "6,000 tokens move from read to write: -$0.02070. The mask has ended, so the break has no ChangeID "
                "(§5.2 row 1). The old figure kept crediting the mask here.",
                DIMS("rewrite", "beforeBStar", "hit-break", **{"break": "plugin", "restore": "in-place"}), [s0, s1, s2])


# Case 17 (#584): the client shrinks its own body.
def c17():
    msgs = [("m0", 5000), ("m1", 5000)]
    s0 = sim(0, "r0", (TOOLS_C, SYS3, msgs), (TOOLS_C, SYS3, msgs), [], "unknown")
    m2 = msgs + [("m2", 2000)]
    s1 = sim(40, "r1", (TOOLS_F, SYS3, m2), (TOOLS_F, SYS3, m2), [], "hot", prev=(33000, 33000),
             usage=U(write=27000), base=U(write=27000),
             effects=[effect("break", "",
                             note="the client dropped tool declarations by itself: no client breakpoint precedes the "
                                  "change, so B* is 0 and the break moves no tokens")])
    write_issue("client-edit/shrink-no-operator", 584,
                "The client drops three tool declarations (8,000 tokens) by itself, with no context operator. The cache "
                "breaks from tools[2], so the provider reads nothing and writes all 27,000 tokens. Nothing a context "
                "operator did is involved: no operation, no operator gets a saving, and the break has an empty ChangeID. "
                "What the client removes is not counted as saved.",
                DIMS("none", "tools", "break", **{"break": "client"}), [s0, s1],
                open_=[BSTAR_SNAP, "Issue 584 option 2 (credit a removal our own "
                       "recommendation caused) needs a reporter for that and is not covered"])


# Case 18 (#696): two operations whose bytes the library converts to tokens.
def c18():
    msgs_c = [("m0", 4000), ("m1", 3000)]
    msgs_f = [("m0", 4000), ("m1f", 500)]
    MK18 = "cg:mask:be02"
    s = sim(0, "r0", (TOOLS_C, SYS3, msgs_c), (TOOLS_F, SYS3, msgs_f),
            [eop(TP, saved=TT(cacheWrite5m=8000)), eop(MK18, saved=TT(cacheWrite5m=2500))], "unknown",
            add=[prune_op(), rewrite("o2", "context-guru.mask", "messages", 1, 3000, 500, MK18)])
    write_issue("estimate/bytes-ratio-json-removal", 696,
                "tool-prune and mask report bytes only. The library converts them with the request ratio (4 bytes a "
                "token here), so both operations are marked Estimated. The library does not correct the conversion: "
                "the host passes the billed total and the conservation check compares it. Issue 696 (a per-family "
                "factor that over-credits a removal) is answered by the request ratio; this case pins the marking.",
                DIMS("rewrite", "mixed", "miss", operations="2", estimate="bytes-ratio"), [s],
                open_=["not tested: a removal denser in tokens than the request average (the other half of issue 696). "
                       "Sizes here are exactly 4 bytes per token, so the conservation check has nothing to report",
                       "design §11 question 3: whether Size should take a content class, or an optional Tokens field"])


# ---------------------------------------------------------------------------------------------
# Cases from docs/more_examples.md. The scenario ids (A1, B3, ...) are that document's.
# The document was drafted before any of them was priced; the ones that do not hold up are listed as
# skipped in its section 6, with the reason.
def write_more(cid, sid, description, dims, steps, open_=None):
    write(case(cid, description, f"more_examples.md#{sid}", dims, steps, open_=open_),
          cid.replace("/", "-").replace("+", "-"))


PING_USAGE = {"input": 10, "cacheRead": 10000, "cacheWrite": 0, "cacheWrite1h": 0, "output": 1}


def ping_call(**kw):
    u = dict(PING_USAGE)
    u.update(kw)
    return {"host": "api.anthropic.com", "model": "agent-model", "usage": u}


SYS10 = [("sys", 2000)]
MSG10 = [("m0", 3000)] + [(f"m{i}", 1000) for i in range(1, 6)]  # 8000 tokens, 10000 with the system block


# A1: the client edits a message inside the prefix and an operator shortens a new one.
def a1():
    CF1 = "cg:cmdfilter:a1c4"
    sysb = SYS10
    M = [(f"m{i}", 3000) for i in range(9)]
    bp = lambda last: BP(("system", 0), ("messages", 2), ("messages", last))
    s0 = sim(0, "r0", ([], sysb, M), ([], sysb, M), [], "unknown", cbp=bp(8), fbp=bp(8))
    edit = lambda m: [x if x[0] != "m3" else ("m3e", 3000) for x in m]
    s1 = sim(40, "r1", ([], sysb, edit(M) + [("m9", 8000)]), ([], sysb, edit(M) + [("m9f", 1000)]),
             [eop(CF1, saved=TT(cacheWrite5m=7000))], "hot",
             add=[rewrite("o1", "context-guru.cmdfilter", "messages", 9, 8000, 1000, CF1)],
             cbp=bp(9), fbp=bp(9), usage=U(read=11000, write=19000), base=U(read=11000, write=26000),
             boundary=P("messages", 2),
             effects=[effect("break", "", frm=P("messages", 2), to=P("messages", 2),
                             note="the client edited message 3: its last breakpoint before the edit is message 2, "
                                  "so B* is already where the provider stopped reading and no token moves")])
    write_more("break/client-edit-and-operator/hot/5m", "A1",
               "The client edits message 3, inside the cached prefix, and cmdfilter shortens a new 8,000-token output "
               "(message 9) to 1,000 tokens. The client's last breakpoint before the edit is the end of message 2, so "
               "B* is the end of message 2: the provider reads 11,000 tokens and writes the rest, in the client's bill "
               "as well as in the forwarded one. The edit is on the record as a break with no ChangeID and moves no "
               "token, because the client body would have stopped reading at the same place. cmdfilter's 7,000 tokens "
               "are after B*, so they are writes: +7,000 x $3.75 = +$0.02625. The edit does not reduce or raise that figure.",
               DIMS("rewrite", "afterBStarToL", "break", **{"break": "client"}), [s0, s1],
               open_=["design §5.2 row 2 and §6.2: B* snaps to the breakpoint before the edit, which is also where the "
                      "provider stopped reading, so the break moves no tokens. The case expects the effect with an empty "
                      "token count. If the library should add no effect at all when nothing moves, drop it here"])


# A2: equal bodies, a hot clock, and a read that is too low.
def a2():
    m = [(f"m{i}", 3000) for i in range(6)]
    s0 = sim(0, "r0", ([], SYS10, m), ([], SYS10, m), [], "unknown")
    m2 = m + [("m6", 1000)]
    s1 = sim(40, "r1", ([], SYS10, m2), ([], SYS10, m2), [], "hot", prev=(20000, 20000),
             usage=U(read=2000, write=19000), base=U(read=2000, write=19000), boundary=P("messages", 5),
             effects=[effect("break", "", saved=TT(cacheRead=18000), caused=TT(cacheWrite5m=18000),
                             frm=P("system", 0), to=P("messages", 5),
                             note="the entry was lost early; nobody caused it")])
    write_more("break/entry-lost-early/hot/5m", "A2",
               "Two requests 40 seconds apart with equal prefixes. The clock says hot, but the provider read only the "
               "2,000 tokens of the system block and wrote the other 19,000 (the lookback window, an eviction, or "
               "another reason the library cannot see). The break has no ChangeID (§5.2 row 4). It is in the client "
               "body's bill too, so the baseline equals the bill and the total is zero.",
               DIMS("none", "beforeBStar", "break", **{"break": "unknown"}), [s0, s1],
               open_=["design R51 and §6.3 conservation: a break nobody caused is in the baseline bill too, so it is on "
                      "the record and out of the sum (see break/client-edit-inside-prefix/hot/5m)", BSTAR_SNAP])


# A3: a cold request with equal bodies.
def a3():
    m = [(f"m{i}", 3000) for i in range(6)]
    s0 = sim(0, "r0", ([], SYS10, m), ([], SYS10, m), [], "unknown")
    s1 = sim(1500, "r1", ([], SYS10, m + [("m6", 1000)]), ([], SYS10, m + [("m6", 1000)]), [], "cold",
             effects=[effect("break", "", note="the entry expired; B* is 0, so the read extent to B* is empty")])
    write_more("break/expiry-moves-no-tokens/cold/5m", "A3",
               "A request 25 minutes after the last, with an equal prefix. The 5m entry expired, so the provider "
               "reads nothing and writes all 21,000 tokens, in both bills. The expiry is on the record as a break with "
               "no ChangeID (§5.2 row 5), but B* is 0 and every position is already a write, so it moves no token and "
               "the total is zero.",
               DIMS("none", "none", "miss", **{"break": "expiry"}), [s0, s1])


# A4 and B3: OpenAI GPT-5.6, a lifetime that is a minimum.
def a4_b3():
    MK4 = "cg:mask:4a90"
    facts = {"ttl": "30m", "lifetime": "minimum"}
    Cm = [("m0", 3000), ("m1", 6000), ("m2", 3000), ("m3", 3000)]
    Fm = [("m0", 3000), ("m1f", 60), ("m2", 3000), ("m3", 3000)]
    op = rewrite("o1", "context-guru.mask", "messages", 1, 6000, 60, MK4)
    for cid, sid, ping in (("keepalive/no-ping/minimum-lifetime/evidence-hot", "A4", False),
                           ("keepalive/ping/minimum-lifetime/rescue", "B3", True)):
        ses = "s-oa-ping" if ping else "s-oa"
        # The ping variant carries no rewrite: a rescue prices the baseline as an expired entry, and the
        # library does not yet tier an operation under a rescue (see the note in design.md §4.4).
        if ping:
            Fm, op = Cm, None
        s0 = sim(0, "r0", ([], SYS10, Cm), ([], SYS10, Fm), [eop(MK4, saved=TT(cacheWrite5m=5940))] if op else [],
                 "unknown", add=[op] if op else [], facts=facts, session=ses, model="gpt-5.6")
        steps = [s0]
        f1 = dict(facts)
        at = 2400
        if ping:
            KAW = "ka:main:w-oa"
            steps.append(ping_step(1800, ses, KAW, ping_call(cacheRead=17000)))
            f1.update(lastPing=ts(1800), pingRead=P("messages", 3), pingWindow=KAW)
            at = 2100
        if ping:
            # The ping kept a 30-minute entry alive past 30 minutes: 30 minutes is 30 minutes, so
            # without the ping the entry is taken to have expired and the whole body would be written.
            steps.append(sim(at, "r1", ([], SYS10, Cm + [("m4", 500)]), ([], SYS10, Fm + [("m4", 500)]),
                             [], "hot", prev=(17000, 17000), boundary=P("messages", 3), facts=f1, session=ses,
                             model="gpt-5.6", base=U(write=17500),
                             effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=17000), caused=TT(cacheRead=17000),
                                             to=P("messages", 3),
                                             note="without the ping the 30-minute entry would have expired; write becomes read")]))
        else:
            steps.append(sim(at, "r1", ([], SYS10, Cm + [("m4", 500)]), ([], SYS10, Fm + [("m4", 500)]),
                             [eop(MK4, saved=TT(cacheRead=5940), note="the read proves the entry, so the saving is a read")],
                             "hot", prev=(17000, 11060), boundary=P("messages", 3), facts=f1, session=ses, model="gpt-5.6"))
        if ping:
            desc = ("OpenAI GPT-5.6: the entry lifetime is a 30-minute minimum, and the owner decision is that 30 "
                    "minutes is 30 minutes (R45). Turn 1 caches 17,000 tokens. A ping at minute 30 reads them and "
                    "renews the entry. The next request arrives at minute 35, 35 minutes after turn 1 and so past the "
                    "lifetime, but 5 minutes after the ping and so inside it. The rescue is credited: 17,000 x "
                    "($3.75 - $0.30) = +$0.05865. The ping costs 17,000 x $0.30 + 10 x $3.00 + $15.00 per million = "
                    "-$0.00513. Total +$0.05352.")
        else:
            desc = ("OpenAI GPT-5.6, 40 minutes after the last request, past the 30-minute minimum. The gap proves "
                    "nothing, so the phase starts unknown. The provider read 11,060 tokens, which is evidence the entry "
                    "was alive, so the phase becomes hot and mask's replayed 5,940 tokens are priced as a read "
                    "(+$0.00178), not a write (+$0.02228). No ping was sent, so there is no rescue either way.")
        write_more(cid, sid, desc, DIMS("rewrite" if not ping else "rescue", "beforeBStar", "evidence",
                                         lifetime="minimum", keepalive=str(ping).lower()), steps,
                   open_=[BSTAR_SNAP])


# B1 and B2: a ping that does not earn a rescue.
def b1_b2():
    for cid, sid, ping_at, req_at, phase, desc in (
            ("keepalive/ping-inside-ttl/no-rescue/5m", "B1", 280, 290, "hot",
             "Turn 1 caches 10,000 tokens. The keeper pings at 280 seconds, and the next request arrives 10 seconds "
             "later, 290 seconds after turn 1: inside the 5m TTL. Without the ping the entry would have been alive, so "
             "no rescue is credited (§4.4 condition 2). The window holds only the ping, 10,000 x $0.30 + 10 x $3.00 + "
             "1 x $15.00 per million = -$0.00305: a ping that was not needed."),
            ("keepalive/ping-after-entry-expired/no-rescue/5m", "B2", 320, 700, "cold",
             "Turn 1 caches 10,000 tokens. The keeper pings at 320 seconds, after the entry expired at 300: the ping "
             "reads nothing and writes the 10,000 tokens again (10,000 x $3.75 + 10 x $3.00 + 1 x $15.00 per million = "
             "-$0.03755). The next request arrives at 700 seconds, 380 after the ping: past the TTL again. The entry "
             "gap is above the TTL, so the phase is cold, no rescue is credited, and the window is only the ping's cost. "
             "The expiry is on the record as a break that moves no token.")):
        KAW = f"ka:main:w-{sid.lower()}"
        ses = f"s-{sid.lower()}"
        wrote = phase == "cold"
        call = ping_call(cacheRead=0, cacheWrite=10000) if wrote else ping_call()
        s0 = sim(0, "r0", ([], SYS10, MSG10), ([], SYS10, MSG10), [], "unknown", session=ses)
        m2 = MSG10 + [("m6", 500)]
        facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(ping_at), "pingRead": P("messages", 5), "pingWindow": KAW}
        s1 = sim(req_at, "r1", ([], SYS10, m2), ([], SYS10, m2), [], phase, prev=(10000, 10000), facts=facts, session=ses,
                 effects=[effect("break", "", note="the entry expired; moves no token")] if wrote else None)
        write_more(cid, sid, desc, DIMS("rescue", "beforeBStar", "no-rescue", keepalive="true"),
                   [s0, ping_step(ping_at, ses, KAW, call), s1])


# B4: a 1h entry is alive after 9 minutes.
def b4():
    KAW = "ka:main:w-b4"
    ses = "s-b4"
    m2 = MSG10 + [("m6", 500)]
    facts = {"ttl": "1h", "lifetime": "exact", "lastPing": ts(300), "pingRead": P("messages", 5), "pingWindow": KAW}
    s0 = sim(0, "r0", ([], SYS10, MSG10), ([], SYS10, MSG10), [], "unknown", session=ses, cbp=bps(MSG10, "1h"),
             fbp=bps(MSG10, "1h"), usage=U(write=10000, w1h=10000), base=U(write=10000, w1h=10000),
             facts={"ttl": "1h", "lifetime": "exact"})
    s1 = sim(540, "r1", ([], SYS10, m2), ([], SYS10, m2), [], "hot", prev=(10000, 10000), session=ses,
             cbp=bps(m2, "1h"), fbp=bps(m2, "1h"), usage=U(read=10000, write=500, w1h=500),
             base=U(read=10000, write=500, w1h=500), facts=facts)
    write_more("keepalive/ping-on-1h-entry/no-rescue/1h", "B4",
               "The whole prompt is cached at 1h. The keeper pings at 5 minutes and the next request arrives at 9. The "
               "entry lives 60 minutes, so without the ping it would have been alive: no rescue (§4.4 condition 2 uses the "
               "TTL of the breakpoint that wrote the entry, not 5m). The window is only the ping, -$0.00305.",
               DIMS("rescue", "beforeBStar", "no-rescue", ttl="1h", keepalive="true"),
               [s0, ping_step(300, ses, KAW, ping_call()), s1])


# B5: two pings in one window, then one rescue.
def b5():
    KAW = "ka:main:w-b5"
    ses = "s-b5"
    m2 = MSG10 + [("m6", 500)]
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(560), "pingRead": P("messages", 5), "pingWindow": KAW}
    s0 = sim(0, "r0", ([], SYS10, MSG10), ([], SYS10, MSG10), [], "unknown", session=ses)
    s1 = sim(840, "r1", ([], SYS10, m2), ([], SYS10, m2), [], "hot", prev=(10000, 10000), session=ses, facts=facts,
             usage=U(read=10000, write=500), base=U(write=10500),
             effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=10000), caused=TT(cacheRead=10000),
                             to=P("messages", 5), note="the smaller of what the last ping read and what the request read")])
    write_more("keepalive/two-pings/one-rescue/5m", "B5",
               "Turn 1 caches 10,000 tokens. The keeper pings at 280 and 560 seconds, then the request arrives at 840: "
               "past the TTL from turn 1, 280 seconds after the last ping. One rescue is credited, for the smaller of what "
               "the last ping read and what the request read, 10,000 tokens: 10,000 x ($3.75 - $0.30) = +$0.03450. Both "
               "pings cost (2 x $0.00305), so the window nets +$0.02841.",
               DIMS("rescue", "beforeBStar", "rescue", keepalive="true", pings="2"),
               [s0, ping_step(280, ses, KAW, ping_call()), ping_step(560, ses, KAW, ping_call()), s1],
               open_=[BSTAR_SNAP])


# C1: a rewrite after the last breakpoint is input.
def c1_():
    CF = "cg:cmdfilter:c1d0"
    M = [(f"m{i}", 3000) for i in range(9)]
    bpf = BP(("system", 0), ("messages", 8))
    s0 = sim(0, "r0", ([], SYS10, M), ([], SYS10, M), [], "unknown", cbp=bpf, fbp=bpf)
    s1 = sim(40, "r1", ([], SYS10, M + [("m9", 8000)]), ([], SYS10, M + [("m9f", 1000)]),
             [eop(CF, saved=TT(input=7000))], "hot", prev=(29000, 29000), cbp=bpf, fbp=bpf,
             add=[rewrite("o1", "context-guru.cmdfilter", "messages", 9, 8000, 1000, CF)],
             usage=U(read=29000, inp=1000), base=U(read=29000, inp=8000), boundary=P("messages", 8))
    write_more("rewrite/after-last-breakpoint/input/5m", "C1",
               "The client keeps its breakpoints at the end of system and at message 8. A new 8,000-token output "
               "(message 9) sits after the last breakpoint, so the provider bills it as input, not as a cache write. "
               "cmdfilter shortens it to 1,000 tokens and saves 7,000 x $3.00 = +$0.02100, not the $0.02625 of a write.",
               DIMS("rewrite", "afterL", "hit"), [s0, s1], open_=[BSTAR_SNAP])


# C2: no breakpoint anywhere (automatic prefix caching, design §5.1 and §6.2).
def c2_():
    CF = "cg:cmdfilter:c2e7"
    M = [(f"m{i}", 3000) for i in range(9)]
    s0 = sim(0, "r0", ([], SYS10, M), ([], SYS10, M), [], "unknown", cbp=[], fbp=[], usage=U(inp=29000), base=U(inp=29000))
    s1 = sim(40, "r1", ([], SYS10, M + [("m9", 8000)]), ([], SYS10, M + [("m9f", 1000)]),
             [eop(CF, saved=TT(input=7000))], "hot", prev=(29000, 29000), cbp=[], fbp=[],
             add=[rewrite("o1", "context-guru.cmdfilter", "messages", 9, 8000, 1000, CF)],
             usage=U(read=29000, inp=1000), base=U(read=29000, inp=8000), boundary=P("messages", 8))
    write_more("rewrite/no-breakpoint/input/5m", "C2",
               "The body carries no cache_control. The provider caches the prefix by itself, so every position counts "
               "as a breakpoint. The 29,000 tokens of turn 1 are read on turn 2. cmdfilter shortens a new 8,000-token "
               "output (message 9) to 1,000 tokens. It sits after B*, so the provider bills it as input, and the saving "
               "is 7,000 x $3.00 = +$0.02100. B* is message 8, the end of the extent.",
               DIMS("rewrite", "afterL", "none", breakpoints="none"), [s0, s1], open_=[BSTAR_SNAP])


# C2b: no breakpoint anywhere, and a cut inside the cached prefix.
def c2b():
    CF = "cg:cmdfilter:c2b1"
    M = [(f"m{i}", 3000) for i in range(9)]
    F = [x if x[0] != "m2" else ("m2f", 600) for x in M]
    s0 = sim(0, "r0", ([], SYS10, M), ([], SYS10, M), [], "unknown", cbp=[], fbp=[], usage=U(inp=29000), base=U(inp=29000))
    # The provider read the system block and messages 0 and 1: 2,000 + 6,000 = 8,000 tokens. The rest is input.
    s1 = sim(40, "r1", ([], SYS10, M + [("m9", 1000)]), ([], SYS10, F + [("m9", 1000)]),
             [eop(CF, saved=TT(cacheRead=2400))], "hot", prev=(29000, 29000), cbp=[], fbp=[],
             add=[rewrite("o1", "context-guru.cmdfilter", "messages", 2, 3000, 600, CF)],
             usage=U(read=8000, inp=19600), base=U(read=29000, inp=1000), boundary=P("messages", 8),
             effects=[effect("break", CF, saved=TT(cacheRead=18600), caused=TT(input=18600),
                             frm=P("messages", 1), to=P("messages", 8))])
    write_more("rewrite/no-breakpoint/cut-inside-cache/5m", "C2",
               "The body carries no cache_control. cmdfilter cuts message 2 from 3,000 to 600 tokens, inside the cached "
               "prefix. The provider reads 8,000 tokens (to message 1) and bills the rest as input. The 2,400 cut tokens "
               "were read before, so they are saved at the read rate, +2,400 x $0.30. The cut breaks the cache: the 18,600 "
               "tokens from message 1 to B* move from read to input.",
               DIMS("rewrite", "beforeBStar", "none", breakpoints="none"), [s0, s1], open_=[BSTAR_SNAP])


# C5: mask at depth on a cold turn, then warm replays.
def c5():
    MK = "cg:mask:c5a2"
    C = [("m0", 3000), ("m1", 6000)] + [(f"m{i}", 3000) for i in range(2, 6)]
    F = [("m0", 3000), ("m1f", 60)] + [(f"m{i}", 3000) for i in range(2, 6)]
    s0 = sim(0, "r0", ([], SYS10, C), ([], SYS10, C), [], "unknown")
    s1 = sim(1500, "r1", ([], SYS10, C + [("m6", 500)]), ([], SYS10, F + [("m6", 500)]),
             [eop(MK, saved=TT(cacheWrite5m=5940), note="deep history on a cold turn: nothing is cached to break")],
             "cold", add=[rewrite("o1", "context-guru.mask", "messages", 1, 6000, 60, MK)])
    s2 = sim(1560, "r2", ([], SYS10, C + [("m6", 500), ("m7", 500)]), ([], SYS10, F + [("m6", 500), ("m7", 500)]),
             [eop(MK, saved=TT(cacheRead=5940), note="applied again; the client's prefix would be cached")],
             "hot", prev=(23500, 17560), boundary=P("messages", 6))
    write_more("rewrite/deep-on-cold/then-hot-replay/5m", "C5",
               "The entry expired, so on turn 2 (25 minutes later) mask removes 5,940 tokens from message 1, deep in "
               "history. Nothing is cached, so no break is possible and the saving is a write, +5,940 x $3.75 = +$0.02228. "
               "Turn 3, 60 seconds later, replays the change: the forwarded prefix is the one turn 2 cached, so there is "
               "no break, and the same tokens are now a read, +5,940 x $0.30 = +$0.00178. The total is +$0.02406.",
               DIMS("rewrite", "beforeBStar", "miss-hit", replay="true"), [s0, s1, s2], open_=[BSTAR_SNAP])


# D2: the expand tool is injected for the first time, on a warm request.
EXPD = "cg:expand:d2f1"


def d2():
    tools = [("tools", 5000)]
    ft = tools + [("expand", 300)]
    m = [("m0", 4000), ("m1", 4000), ("m2", 4000)]
    s0 = sim(0, "r0", (tools, SYS10, m), (tools, SYS10, m), [], "unknown")
    m1 = m + [("m3", 1000)]
    s1 = sim(60, "r1", (tools, SYS10, m1), (ft, SYS10, m1), [eop(EXPD, caused=TT(cacheRead=300))], "hot",
             add=[rewrite("o1", "context-guru.expand", "tools", 1, 0, 300, EXPD)], prev=(19000, 19000),
             usage=U(write=20300), boundary=P("messages", 2),
             effects=[effect("break", EXPD, saved=TT(cacheRead=19300), caused=TT(cacheWrite5m=19300), to=P("messages", 2),
                             note="the declaration is appended to tools: the provider reads nothing")])
    m2 = m1 + [("m4", 500)]
    s2 = sim(120, "r2", (tools, SYS10, m2), (ft, SYS10, m2), [eop(EXPD, caused=TT(cacheRead=300))], "hot",
             prev=(20000, 20300), boundary=P("messages", 3))
    write_more("rewrite/expand-first-injection/break-once/5m", "D2",
               "On turn 2 the engine appends the expand tool (300 tokens) to tools for the first time. tools come first in "
               "the prompt and the body has no breakpoint on them, so the provider reads nothing: the 19,300 tokens move "
               "from read to write, 19,300 x ($3.75 - $0.30) = -$0.06659, and the declaration itself is a cost of 300 x "
               "$0.30 = -$0.00009. The tool is sticky, so turn 3 carries it again with no break and costs -$0.00009. The "
               "total is -$0.06677.",
               DIMS("rewrite", "tools", "break", **{"break": "plugin", "sticky": "true"}), [s0, s1, s2],
               open_=["the expand tool is reported here as a rewrite with SizeAfter above SizeBefore (§4.2 rule 3, "
                      "cortex-wiring.md). Whether the engine reports it at all is not checked; if it does not, the break "
                      "on turn 2 has no ChangeID", BSTAR_SNAP])


# D3: toolschema is turned on mid-session.
def d3():
    TS = "cg:toolschema:d3b7"
    tc = [("t0", 5000), ("t1", 3000), ("t2", 4000)]
    tf = [("t0", 5000), ("t1f", 1000), ("t2", 4000)]
    m = [(f"m{i}", 4000) for i in range(3)]
    s0 = sim(0, "r0", (tc, SYS10, m), (tc, SYS10, m), [], "unknown")
    m1 = m + [("m3", 1000)]
    s1 = sim(60, "r1", (tc, SYS10, m1), (tf, SYS10, m1), [eop(TS, saved=TT(cacheRead=2000))], "hot",
             add=[rewrite("o1", "context-guru.toolschema", "tools", 1, 3000, 1000, TS)], prev=(26000, 26000),
             usage=U(write=25000), boundary=P("messages", 2),
             effects=[effect("break", TS, saved=TT(cacheRead=24000), caused=TT(cacheWrite5m=24000), to=P("messages", 2),
                             note="a tool declaration changed: the provider reads nothing")])
    m2 = m1 + [("m4", 500)]
    s2 = sim(120, "r2", (tc, SYS10, m2), (tf, SYS10, m2), [eop(TS, saved=TT(cacheRead=2000))], "hot",
             prev=(27000, 25000), boundary=P("messages", 3))
    write_more("rewrite/toolschema-mid-session/break-then-saving/5m", "D3",
               "toolschema is turned on at turn 2 and trims one tool declaration from 3,000 to 1,000 tokens. The "
               "declaration is part of the cached tools, so the provider reads nothing: 24,000 forwarded tokens move from "
               "read to write, -$0.08280, against a saving of 2,000 x $0.30 = +$0.00060. Turn 3 replays the change with no "
               "break and saves +$0.00060 again, and every later turn does too. The net after two turns is -$0.08160.",
               DIMS("rewrite", "tools", "break-hit", **{"break": "plugin", "replay": "true"}), [s0, s1, s2],
               open_=[BSTAR_SNAP])


# D4: a summary on a cold turn.
SUD4 = "cg:summarize:d4c9"


def summary_bodies():
    tools, sysb = [("tools4", 4000)], [("sys4", 4000)]
    old = [("m0", 4000), ("m1", 3000), ("m2", 3000), ("m3", 1500), ("m4", 1500)]
    return tools, sysb, old


def d4():
    tools, sysb, old = summary_bodies()
    summary, tail = [("summary", 500)], old[3:]
    spend = {"id": "op1", "kind": "spend", "operator": "context-guru.summarize", "changeId": SUD4,
             "call": {"model": "summarizer-model", "params": {"effort": "low"},
                      "usage": {"input": 10000, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": 500, "costUSD": 0.0125}}}
    rw = rewrite("op2", "context-guru.summarize", "messages", 0, 10000, 500, SUD4, splice={"at": 0, "removed": 3, "inserted": 1})
    s0 = sim(0, "r0", (tools, sysb, old), (tools, sysb, old), [], "unknown")
    s1 = sim(1500, "r1", (tools, sysb, old + [("n0", 500)]), (tools, sysb, summary + tail + [("n0", 500)]),
             [eop(SUD4, call=spend["call"]), eop(SUD4, saved=TT(cacheWrite5m=9500))], "cold", add=[spend, rw])
    s2 = sim(1560, "r2", (tools, sysb, old + [("n0", 500), ("n1", 500)]),
             (tools, sysb, summary + tail + [("n0", 500), ("n1", 500)]),
             [eop(SUD4, saved=TT(cacheRead=9500), note="applied again; the summary is cached now")],
             "hot", prev=(21500, 12000), boundary=P("messages", 5))
    write_more("rewrite+spend/summary/cold-no-break/5m", "D4",
               "The contrast of the summary case that breaks the cache. The same summary (messages 0 to 2, 10,000 tokens "
               "to 500) is made on a request 25 minutes after the last, when nothing is cached. There is no prefix to "
               "break, so no break is charged: the 9,500 tokens are writes, +$0.03563, against the call, -$0.01250. "
               "Turn 3 replays it as a read, +$0.00285. The total is +$0.02598.",
               DIMS("rewrite+spend", "beforeBStar", "miss-hit", ttl="5m", replay="true"), [s0, s1, s2], open_=[BSTAR_SNAP])


# D5: the sweep when the ask cannot read the cached prefix.
def d5():
    SW = "cg:extract_llm_sweep:9d44"
    base = [("m0", 3000), ("m1", 8000), ("m2", 1000), ("m3", 2000), ("m4", 1000), ("m5", 1000)]
    s0 = sim(0, "r0", ([], SYS10[:1] and [("sys9", 2000)], base), ([], [("sys9", 2000)], base), [], "unknown", session="s-d5")
    m6 = base + [("m6", 500)]
    s1 = sim(100, "r1", ([], [("sys9", 2000)], m6), ([], [("sys9", 2000)], m6), [], "hot", prev=(18000, 18000), session="s-d5")
    call = {"host": "api.anthropic.com", "model": "agent-model",
            "usage": {"input": 18900, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": 200}}
    ask = {"version": 1, "requestId": "sw-ask-fb", "session": "s-d5", "thread": {"id": "main"}, "at": ts(380),
           "billing": {"mode": "api"}, "ops": [{"id": "op1", "kind": "spend", "operator": "context-guru.extract_llm_sweep",
                                                 "changeId": SW, "call": call}]}
    spans = [{"section": "messages", "from": 1, "to": 1, "sizeBefore": size(8000), "sizeAfter": size(100)},
             {"section": "messages", "from": 3, "to": 3, "sizeBefore": size(2000), "sizeAfter": size(50)}]
    rw = rewrite("op2", "context-guru.extract_llm_sweep", "messages", 1, 10000, 150, SW, spans=spans)
    c2 = m6 + [("m7", 500)]
    f2 = [("m0", 3000), ("m1f", 100), ("m2", 1000), ("m3f", 50), ("m4", 1000), ("m5", 1000), ("m6", 500), ("m7", 500)]
    s2 = sim(1800, "r2", ([], [("sys9", 2000)], c2), ([], [("sys9", 2000)], f2),
             [eop(SW, saved=TT(cacheWrite5m=9850), note="two spans, one ChangeID")], "cold", add=[rw], session="s-d5")
    write_more("rewrite+spend/extract-llm-sweep/fallback-ask-fresh-input/5m", "D5",
               "The sweep's ask could not read the cached prefix (after a restart the proxy holds no previous body), so it "
               "falls back to a call that pays the whole 18,500-token transcript plus the 400-token question as fresh input: "
               "18,900 x $3.00 + 200 x $15.00 per million = -$0.05970. The ask drops two outputs, and the next cold request "
               "removes 7,900 + 1,950 = 9,850 tokens as writes, +$0.03694, as two spans of one change. The sweep nets "
               "-$0.02276: with the prefix the same ask would have cost -$0.00945 (see "
               "rewrite+spend/extract-llm-sweep/ask-then-cold-apply/5m).",
               DIMS("rewrite+spend", "beforeBStar", "hit-ask-miss", keepalive="true", sweep="fallback", spans="true"),
               [s0, s1, {"standalone": ask, "usage": U(), "expected": {"ops": [eop(SW, call=call)]}}, s2])


# D6: cache_aware_summarizer reads the prefix the agent's model holds.
def d6():
    SUC = "cg:cache_aware_summarizer:3e55"
    tools, sysb, old = summary_bodies()
    summary, tail = [("summary", 500)], old[3:]
    s0 = sim(0, "r0", (tools, sysb, old), (tools, sysb, old), [], "unknown", session="s-d6")
    call = {"host": "api.anthropic.com", "model": "agent-model",
            "usage": {"input": 300, "cacheRead": 21000, "cacheWrite": 0, "cacheWrite1h": 0, "output": 500}}
    ask = {"version": 1, "requestId": "cas-1", "session": "s-d6", "thread": {"id": "main"}, "at": ts(280),
           "billing": {"mode": "api"}, "ops": [{"id": "op1", "kind": "spend", "operator": "context-guru.cache_aware_summarizer",
                                                 "changeId": SUC, "call": call}]}
    rw = rewrite("op2", "context-guru.cache_aware_summarizer", "messages", 0, 10000, 500, SUC,
                 splice={"at": 0, "removed": 3, "inserted": 1})
    s1 = sim(1800, "r1", (tools, sysb, old + [("n0", 500)]), (tools, sysb, summary + tail + [("n0", 500)]),
             [eop(SUC, saved=TT(cacheWrite5m=9500))], "cold", add=[rw], session="s-d6")
    write_more("rewrite+spend/cache-aware-summarizer/prefix-read/5m", "D6",
               "cache_aware_summarizer in pre_expiry mode makes its call in place of the keep-alive ping, on the agent's "
               "own model, reading the 21,000-token cached prefix: 300 input, 21,000 read, 500 output = -$0.01470. "
               "Summarize (the case summary/break-then-payback) pays 10,000 tokens of fresh input at a cheaper model's "
               "rates, -$0.01250. The summary is held until the cache is cold: 25 minutes later it replaces messages 0 to 2 "
               "and the 9,500 tokens are writes, +$0.03563. The ask and the removal share one ChangeID. Total +$0.02093.",
               DIMS("rewrite+spend", "beforeBStar", "hit-ask-miss", keepalive="true", summarizer="cache-aware"),
               [s0, {"standalone": ask, "usage": U(), "expected": {"ops": [eop(SUC, call=call)]}}, s1])


# E2: cachesplit, the volatile tail unchanged.
def e2():
    SPL2 = "cg:cachesplit:e2a8"
    msgs0 = [("m0", 5000)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3300, 3300, SPL2)
    fsys = [("sysstable", 3000), ("sysvol", 300)]
    s0 = sim(0, "r0", ([], [("sysblk", 3300)], msgs0), ([], fsys, msgs0), [], "unknown", add=[op],
             cbp=BP(("system", 0), ("messages", 0)), fbp=BP(("system", 0), ("messages", 0)))
    m1 = msgs0 + [("m1", 1000)]
    s1 = sim(40, "r1", ([], [("sysblk", 3300)], m1), ([], fsys, m1), [], "hot", prev=(8300, 8300),
             cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", 0), ("messages", 1)), boundary=P("messages", 0))
    write_more("split/tail-unchanged/no-effect", "E2",
               "cachesplit moved the breakpoint to the end of the stable half of the system block, but the volatile tail "
               "does not change between the two requests. The client body would be read in full, and so is the "
               "forwarded one: no split effect, no saving, a zero total. The split costs nothing when it is not needed.",
               DIMS("setting-breakpoint", "system", "no-effect"), [s0, s1])


# E3, E4, E5: headttl.
def head_steps(second):
    HT = "headttl:main:sys3a1c"
    head = [("sys", 3000)]
    m0 = [("m0", 5000)]
    cb = BP(("system", 0), ("messages", 0))
    fb = [{"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 0), "ttl": "5m"}]
    op = rewrite("o1", "context-guru.headttl", "system", 0, 3000, 3000, HT)
    s0 = sim(0, "r0", ([], head, m0), ([], head, m0), [], "unknown", add=[op], cbp=cb, fbp=fb,
             usage=U(write=8000, w1h=3000), base=U(write=8000), facts={"ttl": "1h", "lifetime": "exact"},
             effects=[effect("ttlPremium", HT, saved=TT(cacheWrite5m=3000), caused=TT(cacheWrite1h=3000), ttl="1h",
                             note="the head is written at 1h, whether or not anyone reads it")])
    steps = [s0]
    if second is not None:
        sec, rescue = second
        m1 = m0 + [("m1", 500)]
        cb1, fb1 = BP(("system", 0), ("messages", 1)), [{"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 1), "ttl": "5m"}]
        if rescue:
            kw = dict(usage=U(read=3000, write=5500), base=U(write=8500),
                      effects=[effect("ttlRescue", HT, saved=TT(cacheWrite5m=3000), caused=TT(cacheRead=3000), ttl="1h",
                                      note="the 5m entry would have expired; the 1h head is read")])
        else:
            kw = dict(usage=U(read=8000, write=500), base=U(read=8000, write=500))
        steps.append(sim(sec, "r1", ([], head, m1), ([], head, m1), [], "hot", prev=(8000, 8000), cbp=cb1, fbp=fb1,
                         facts={"ttl": "1h", "lifetime": "exact"}, **kw))
    return steps


def e345():
    write_more("ttl/head-1h/read-after-20-minutes", "E3",
               "headttl holds the 3,000-token head of the request for 1h. Writing it costs the premium over a 5m write, "
               "3,000 x ($6.00 - $3.75) = -$0.00675 (ttlPremium). Twenty minutes later the client's 5m entry would have "
               "expired, but the 1h head is read: 3,000 x ($3.75 - $0.30) = +$0.01035 (ttlRescue). The total is +$0.00360.",
               DIMS("setting-breakpoint", "system", "premium-rescue", ttl="1h"), head_steps((1200, True)))
    write_more("ttl/head-1h/never-read", "E4",
               "headttl writes the 3,000-token head at 1h and no request reads it again. Only the cost leg exists: "
               "3,000 x ($6.00 - $3.75) = -$0.00675. The bet's total is negative and visible, as a keep-alive window that "
               "rescued nothing is.",
               DIMS("setting-breakpoint", "system", "premium-only", ttl="1h"), head_steps(None))
    write_more("ttl/head-1h/read-after-3-minutes", "E5",
               "headttl writes the head at 1h and the next request reads it 3 minutes later. The 5m entry would have been "
               "alive too, so the 1h read is worth nothing extra: no ttlRescue. The premium is paid, -$0.00675, and "
               "nothing pays it back.",
               DIMS("setting-breakpoint", "system", "premium-no-rescue", ttl="1h"), head_steps((180, False)))


# F2, F4, F5: settings.
def setting_steps(change, settings, usage1, effects, unpriced=None, cbp=None, model="agent-model", params=None,
                  facts=None, params_from=None):
    tools, sysb = [("tools5", 5000)], [("sys5", 3000)]
    msgs = [(f"m{i}", 8000) for i in range(4)]
    bp0 = cbp(3) if cbp else None
    s0 = sim(0, "r0", (tools, sysb, msgs), (tools, sysb, msgs), [], "unknown", cbp=bp0, fbp=bp0, session="s-set",
             model=model, params=params_from, facts=facts)
    m5 = msgs + [("m4", 1000)]
    bp1 = cbp(4) if cbp else None
    setting = {"id": "op1", "kind": "setting", "operator": "inference-router", "changeId": change, "settings": settings}
    s1 = sim(60, "r1", (tools, sysb, m5), (tools, sysb, m5), [], "hot", add=[setting], prev=(40000, 40000), cbp=bp1, fbp=bp1,
             usage=usage1, boundary=P("messages", 3), effects=effects, unpriced=unpriced, session="s-set", model=model,
             params=params, facts=facts)
    return [s0, s1]


def f245():
    # F2: the web-search toggle sits at system[0]. A breakpoint on tools keeps the tools readable.
    cb = lambda last: BP(("tools", 0), ("system", 0), ("messages", last))
    steps = setting_steps("rt:websearch-1d2f", {"web_search": {"from": "off", "to": "on", "at": P("system", 0)}},
                          U(read=5000, write=36000),
                          [effect("break", "rt:websearch-1d2f", saved=TT(cacheRead=35000), caused=TT(cacheWrite5m=35000),
                                  frm=P("tools", 0), to=P("messages", 3), note="35,000 tokens move from read to write")],
                          cbp=cb)
    write_more("setting/web-search-toggle/break-from-system", "F2",
               "A router turns web search on for request 5. Anthropic renders the toggle into the system prompt, so the "
               "tools stay valid and the system and messages do not: with a breakpoint on tools the provider reads 5,000 "
               "tokens and writes the other 36,000. The setting carries system[0]. 35,000 x ($3.75 - $0.30) = -$0.12075.",
               DIMS("setting", "system", "break", setting="web_search", **{"break": "setting"}), steps,
               open_=[BSTAR_SNAP, "design §4.2 setting rules: system[0] carries a breakpoint of its own, so the last breakpoint "
                      "strictly before it is the one on tools. The case adds that breakpoint; with only system and last-message "
                      "breakpoints the read would be 0"])
    # F4: OpenAI effort change; the usage shows the whole prefix read, so no break.
    steps = setting_steps("rt:effort-4c1a", {"effort": {"from": "high", "to": "low", "at": P("tools", 0)}},
                          U(read=40000, write=1000), [], model="gpt-5.6", params={"effort": "low"},
                          params_from={"effort": "high"}, facts={"ttl": "30m", "lifetime": "minimum"})
    write_more("setting/effort-change/read-intact-no-break", "F4",
               "A router lowers the effort of an OpenAI request. The setting names tools[0] as the point where a break "
               "could start, but the provider kept the entry: it read all 40,000 tokens. At is where a break would start, "
               "not a statement that one happened (§4.2 rule 2), and row 3 of §5.2 needs a read below the prediction. No "
               "effect, no cost, a zero total.",
               DIMS("setting", "tools", "hit", setting="effort", **{"break": "none"}), steps)
    # F5: a setting with no position.
    steps = setting_steps("rt:effort-5d70", {"effort": {"from": "high", "to": "low"}}, U(read=8000, write=33000),
                          [effect("break", "rt:effort-5d70", saved=TT(cacheRead=32000), caused=TT(cacheWrite5m=32000),
                                  frm=P("system", 0), to=P("messages", 3),
                                  note="from the read the usage shows; the position of the cause is unknown")],
                          unpriced=[{"opId": "op1", "reason": "penalty_unknown"}], params={"effort": "low"},
                          params_from={"effort": "high"})
    write_more("setting/nil-position/penalty-unknown", "F5",
               "The router lowered effort, but its reporter does not know where the provider renders it, so the setting "
               "has no position. The provider read 8,000 tokens and wrote 33,000. With no At the library takes the read "
               "the usage shows as the end of the valid prefix: the break covers 32,000 tokens, -$0.11040, and the "
               "operation is reported unpriced with penalty_unknown. The figure is a floor, so a host must show it as "
               "an estimate (§6.4).",
               DIMS("setting", "nil", "break", setting="effort", **{"break": "setting"}), steps, open_=[BSTAR_SNAP])


# H2: a summary splices the message list and a later operator reports in the spliced positions.
def h2():
    SUH, CFH = "cg:summarize:h2c3", "cg:cmdfilter:h2d6"
    tools, sysb, old = summary_bodies()
    summary, tail = [("summary", 500)], old[3:]
    spend = {"id": "op1", "kind": "spend", "operator": "context-guru.summarize", "changeId": SUH,
             "call": {"model": "summarizer-model", "params": {"effort": "low"},
                      "usage": {"input": 10000, "cacheRead": 0, "cacheWrite": 0, "cacheWrite1h": 0, "output": 500, "costUSD": 0.0125}}}
    rw = rewrite("op2", "context-guru.summarize", "messages", 0, 10000, 500, SUH, splice={"at": 0, "removed": 3, "inserted": 1})
    cm = rewrite("op3", "context-guru.cmdfilter", "messages", 3, 6000, 600, CFH)
    s0 = sim(0, "r0", (tools, sysb, old), (tools, sysb, old), [], "unknown")
    s1 = sim(1500, "r1", (tools, sysb, old + [("n0", 6000)]), (tools, sysb, summary + tail + [("n0f", 600)]),
             [eop(SUH, call=spend["call"]), eop(SUH, saved=TT(cacheWrite5m=9500)), eop(CFH, saved=TT(cacheWrite5m=5400))],
             "cold", add=[spend, rw, cm])
    write_more("rewrite+spend/summary-then-cmdfilter/positions/5m", "H2",
               "On a cold request the summary replaces messages 0 to 2 with one (a splice of 3 removed, 1 inserted), and "
               "cmdfilter, which runs after it, reports its 6,000-token output at index 3 of the summary's output: client "
               "message 5. The order check maps that index back through the splice. Both removals are writes: 9,500 and "
               "5,400 tokens, +$0.05588, against the summarizer's call -$0.01250. Total +$0.04338.",
               DIMS("rewrite+spend", "mixed", "miss", operations="2", splice="true"), [s0, s1], open_=[BSTAR_SNAP])


# H5: what the library cannot price, and what it must still notice.
def h5():
    sysb = [("sys", 2000)]
    m = [(f"m{i}", 3000) for i in range(4)]
    bogus = {"id": "op1", "kind": "quantum", "operator": "context-guru.quantum", "changeId": "cg:quantum:5a0e"}
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown", add=[bogus],
             unpriced=[{"opId": "op1", "reason": "unknown_kind"}])
    mc = m + [("m4", 6000)]
    mf = m + [("m4f", 1000)]
    s1 = sim(40, "r1", ([], sysb, mc), ([], sysb, mf), [], "hot", prev=(14000, 14000), checks=["sum", "conservation"])
    write_more("unpriced/unknown-kind-and-unreported-edit", "H5",
               "A newer operator reports an operation of a kind this library does not know: it is carried and reported "
               "unpriced (unknown_kind), and prices to nothing. On the next request a plugin shortens message 4 by 5,000 "
               "tokens and reports nothing. The client body is 5,000 tokens larger than the forwarded one with no "
               "operation to account for it, so the sum check fails, and the bill difference matches no operation, so "
               "the conservation check fails. Pricing goes on.",
               DIMS("none", "afterBStar", "hit", check="sum,conservation"), [s0, s1])


# C3: one failed_run change removes two runs, one inside the cached prefix and one new.
def c3_():
    FR = "cg:failed_run:c3f2"
    M = [("m0", 3000), ("m1", 4000)] + [(f"m{i}", 3000) for i in range(2, 7)]
    bp = lambda last: BP(("system", 0), ("messages", 6), ("messages", last))
    s0 = sim(0, "r0", ([], SYS10, M), ([], SYS10, M), [], "unknown", cbp=bp(6), fbp=bp(6))
    tail = [("m7", 5000)]
    F = [M[0], ("m1f", 100)] + M[2:] + [("m7f", 200)]
    spans = [{"section": "messages", "from": 1, "to": 1, "sizeBefore": size(4000), "sizeAfter": size(100)},
             {"section": "messages", "from": 7, "to": 7, "sizeBefore": size(5000), "sizeAfter": size(200)}]
    op = rewrite("o1", "context-guru.failed_run", "messages", 1, 9000, 300, FR, spans=spans)
    s1 = sim(40, "r1", ([], SYS10, M + tail), ([], SYS10, F), [eop(FR, saved=TT(cacheRead=3900, cacheWrite5m=4800))], "hot",
             add=[op], cbp=bp(7), fbp=bp(7), prev=(24000, 24000), usage=U(read=2000, write=18300),
             base=U(read=24000, write=5000), boundary=P("messages", 6),
             effects=[effect("break", FR, saved=TT(cacheRead=18100), caused=TT(cacheWrite5m=18100),
                             frm=P("system", 0), to=P("messages", 6), note="message 1 is inside the cached prefix")])
    write_more("rewrite/two-spans-two-tiers/hot/5m", "C3",
               "failed_run removes two failed runs as one change with two spans: message 1 (4,000 to 100 tokens), inside the "
               "prefix the last turn cached, and a new message 7 (5,000 to 200 tokens). The first span would have been "
               "read and the second written, so one change is priced at two tiers: 3,900 x $0.30 + 4,800 x $3.75 = "
               "+$0.01917. Message 1 is also inside the cached prefix, so the provider reads only the 2,000 tokens of the "
               "system block and writes the 18,300 after it: the break moves 18,100 tokens, -$0.06245. Net -$0.04328.",
               DIMS("rewrite", "mixed", "break", spans="true", **{"break": "plugin"}), [s0, s1],
               open_=["design §6.2: the break starts at the read the usage shows, here the end of system; the case takes the "
                      "client breakpoints as system, message 6 and the last message", BSTAR_SNAP])


# ---------------------------------------------------------------------------------------------
# Cases from the review of rossoctl/context-guru#768 (library issues #9, #14, #20, #21). Each case
# states the figure the spec requires. The library does not meet it yet, so each fails today; the
# issue named in `source` is the fix. They are written from the spec and the issue's own numbers.
ISSUES_768 = "issue_cases.md"


def write_review(cid, issue, description, dims, steps, open_=None):
    write(case(cid, description, f"{ISSUES_768}#accountant-{issue}", dims, steps, open_=open_),
          cid.replace("/", "-").replace("+", "-"))


# accountant#9: the rescue is the smaller of what the ping read and what the request read (design
# §4.4 condition 4). The tokens the request WRITES do not enter the rule. Condition 3 ("the provider
# read more than it wrote") compared the read with the write of the whole request, so a long turn
# removed a rescue the ping had earned.
def r9():
    for written, cid in ((500, "keepalive/ping-rescue-small-turn/5m"), (6000, "keepalive/ping-rescue-write-equals-read/5m"),
                         (20000, "keepalive/ping-rescue-large-turn/5m")):
        sysb, m0 = [("sys", 500)], [("m0", 2750), ("m1", 2750)]  # 6,000 tokens with the system block
        KAW = f"ka:main:w-r9-{written}"
        ses = f"s-r9-{written}"
        call = ping_call(cacheRead=6000, input=5)
        s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown", session=ses)
        m1 = m0 + [("m2", written)]
        facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(240), "pingRead": P("messages", 1), "pingWindow": KAW}
        s1 = sim(480, "r1", ([], sysb, m1), ([], sysb, m1), [], "hot", prev=(6000, 6000), facts=facts, session=ses,
                 usage=U(read=6000, write=written), base=U(write=6000 + written), boundary=P("messages", 1),
                 effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=6000), caused=TT(cacheRead=6000), to=P("messages", 1),
                                 note="the ping kept 6,000 tokens alive; the request read them")])
        write_review(cid, 9,
                     f"Turn 0 caches 6,000 tokens. A ping at minute 4 reads all 6,000. The request at minute 8 reads the "
                     f"same 6,000 and writes {written:,} new tokens. Without the ping the entry would have expired at "
                     f"minute 5, so the request would have written 6,000 + {written:,}. The rescue is the smaller of what the "
                     f"ping read and what the request read: 6,000 x ($3.75 - $0.30) = +$0.02070, whatever the new turn "
                     f"wrote (design §4.4 condition 4).",
                     DIMS("rescue", "beforeBStar", "rescue", keepalive="true", written=str(written)),
                     [s0, ping_step(240, ses, KAW, call), s1])


# accountant#53: the rescue is the smaller of what the ping read and what the request read (design §4.4
# condition 4), with a big write on the request. Turn 0 caches 6,000 tokens: system 2,000, m0 2,000, m1 2,000.
def issue53(cid, ping_read_tokens, ping_pos, req_read, written, ttl, ping_at, req_at, rescued, text, edit=False,
            base_read=None, open_=None):
    ses = "s-" + cid.replace("/", "-")
    KAW = "ka:main:w-i53-" + cid.split("/")[-1]
    sysb = [("sys", 2000)]
    m0 = [("m0", 2000), ("m1", 2000)]
    one_h = ttl == "1h"
    tier = (lambda n: dict(cacheWrite1h=n)) if one_h else (lambda n: dict(cacheWrite5m=n))
    bp = lambda msgs: bps(msgs, ttl)
    w = lambda r, n: U(read=r, write=n, w1h=n) if one_h else U(read=r, write=n)
    facts0 = {"ttl": ttl, "lifetime": "exact"}
    s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown", session=ses, cbp=bp(m0), fbp=bp(m0),
             usage=w(0, 6000), base=w(0, 6000), facts=facts0)
    # An edit changes message 0: the provider then finds only the system block. Otherwise the request appends.
    m1 = ([("m0e", 2000), ("m1", 2000)] if edit else m0) + [("m2", written - 2000 * edit * 2)]
    facts = dict(facts0)
    if ping_at is not None:
        facts.update({"pingWindow": KAW, "lastPing": ts(ping_at), "pingRead": ping_pos})
    total = tokens(sysb, m1)
    use = w(req_read, total - req_read)
    base_read = req_read - rescued if base_read is None else base_read
    base = w(base_read, total - base_read)
    effects = None
    boundary = ping_pos if rescued < req_read else P("system", 0) if req_read == 2000 else P("messages", 1)
    if rescued:
        effects = [effect("rescue", KAW, saved=TT(**tier(rescued)), caused=TT(cacheRead=rescued), to=ping_pos)]
        if edit:
            effects.append(effect("break", "", frm=P("system", 0), to=P("system", 0)))
    s1 = sim(req_at, "r1", ([], sysb, m1), ([], sysb, m1), [], "hot", session=ses, cbp=bp(m1), fbp=bp(m1), prev=(6000, 6000),
             usage=use, base=base, facts=facts, effects=effects,
             boundary=P("system", 0) if edit else boundary)
    steps = [s0]
    if ping_at is not None:
        steps.append(ping_step(ping_at, ses, KAW, ping_call(cacheRead=ping_read_tokens, input=5)))
    steps.append(s1)
    write_review(cid, 53, text, DIMS("rescue", "beforeBStar", "rescue" if rescued else "no-rescue", ttl=ttl,
                                     keepalive="true" if ping_at is not None else "false"), steps, open_=open_)


def i53():
    issue53("keepalive/ping-rescue-request-reads-less/5m", 6000, P("messages", 1), 2000, 20000, "5m", 240, 480, 2000,
            "Turn 0 caches 6,000 tokens. A ping at minute 4 reads all 6,000. At minute 8 a client edit changes message 0, "
            "so the request reads only the 2,000-token system block and writes 20,000. The rescue is the smaller of the "
            "two reads: 2,000 x ($3.75 - $0.30) = +$0.00690. The large write does not remove it (design §4.4 condition 4).",
            edit=True)
    issue53("keepalive/ping-rescue-request-reads-more/5m", 4000, P("messages", 0), 6000, 20000, "5m", 240, 480, 4000,
            "Turn 0 caches 6,000 tokens. A ping at minute 4 reads 4,000 of them. The request at minute 8 reads 6,000 "
            "and writes 20,000. The rescue is the smaller of the two reads: 4,000 x ($3.75 - $0.30) = +$0.01380.")
    issue53("keepalive/ping-rescue-large-turn/1h", 6000, P("messages", 1), 6000, 20000, "1h", 58 * 60, 70 * 60, 6000,
            "Turn 0 caches 6,000 tokens at 1h. A ping at minute 58 reads all 6,000. The request at minute 70 is past the "
            "hour from turn 0, reads 6,000 and writes 20,000 at 1h. The entry would have expired, so the rescue avoids "
            "a 1h write: 6,000 x ($6.00 - $0.30) = +$0.03420.")
    issue53("keepalive/no-ping-large-turn/no-rescue/5m", 0, None, 6000, 20000, "5m", None, 480, 0,
            "Turn 0 caches 6,000 tokens. No ping is sent. The request at minute 8 reads 6,000 and writes 20,000. "
            "The provider read the entry, so the clock is wrong and the phase is hot (design §5.2). A large write "
            "alone never earns a rescue: no effect.")


# accountant#56: a 1h entry that changes, expires or is downgraded, and keep-alive across the hour.
def i56():
    CH = "cg:ttl:i56"
    cm = [("a", 2000), ("b", 3000), ("c", 2000)]
    fm = [("a", 2000), ("b2", 1000), ("c", 2000)]  # the mask cuts message 1 from 3,000 to 1,000: 2,000 saved
    c1m, f1m = cm + [("d", 500)], fm + [("d", 500)]
    op = rewrite("o1", "mask", "messages", 1, 3000, 1000, CH)
    ses = "s-i56"

    def marks(ttl, i):
        return [{"pos": P("messages", i), "ttl": ttl}]

    def r0(ttl, w1h, saved, billed1h=None):
        b = marks(ttl, 2)
        w = 5000 if billed1h is None else 5000
        return step(0, "r0", body([], [], cm), body([], [], fm), b, b, [op], U(write=5000, w1h=w1h),
                    expect("unknown", U(write=7000, w1h=7000 if ttl == "1h" else 0),
                           [eop(CH, saved=saved, note="first request: nothing is cached")],
                           unpriced=[{"reason": "penalty_unknown"}]), facts={"ttl": ttl, "lifetime": "exact"})

    h1 = lambda n: TT(cacheWrite1h=n)
    DIM = lambda cache, **kw: DIMS("rewrite", "afterBStarToL", cache, ttl="1h", **kw)

    # Row 1: the prefix changes while the 1h entry is alive. No operation: the client edit alone is the break.
    ce = [("a2", 2000), ("b", 3000), ("c", 2000), ("d", 500)]
    mk = marks("1h", 3)
    s0 = step(0, "r0", body([], [], cm), body([], [], cm), marks("1h", 2), marks("1h", 2), [], U(write=7000, w1h=7000),
              expect("unknown", U(write=7000, w1h=7000), [], unpriced=[{"reason": "penalty_unknown"}]),
              facts={"ttl": "1h", "lifetime": "exact"})
    s1 = step(1200, "r1", body([], [], ce), body([], [], ce), mk, mk, [], U(write=7500, w1h=7500),
              expect("hot", U(write=7500, w1h=7500), [],
                     effects=[effect("break", "", note="the client edited message 0; the break moves no tokens")]),
              facts={"ttl": "1h", "lifetime": "exact"})
    write_more("ttl/1h-entry-prefix-changes/hot/1h", "I56a",
               "r0 writes 7,000 tokens at 1h. r1 comes 20 minutes later and the client has edited message 0, so the "
               "provider reads nothing and writes all 7,500 at 1h. The 1h entry is alive, so the phase is hot (not cold "
               "from a 5m expiry), and the lost read is a break nobody caused, in the client body's bill too.",
               DIM("break"), [s0, s1])

    # Row 2: the same entry, the prefix unchanged. The replay is a read.
    s0 = r0("1h", 5000, h1(2000))
    s1 = step(1200, "r1", body([], [], c1m), body([], [], f1m), mk, mk, [], U(read=5000, write=500, w1h=500),
              expect("hot", U(read=7000, write=500, w1h=500), [eop(CH, saved=TT(cacheRead=2000))],
                     boundary=P("messages", 2)), facts={"ttl": "1h", "lifetime": "exact"})
    write_more("ttl/1h-entry-replay/read-after-20-minutes/1h", "I56b",
               "r0 writes 5,000 tokens at 1h; the mask removes 2,000. r1 comes 20 minutes later and reads 5,000. "
               "The entry is alive, so the replayed 2,000 tokens are a read: 2,000 x $0.30/M = $0.00060 (600 micro-dollars), "
               "not a 5m write ($0.00750).", DIM("hit"), [s0, s1])

    # Row 3: the entry really expires.
    s0 = r0("1h", 5000, h1(2000))
    s1 = step(4200, "r1", body([], [], c1m), body([], [], f1m), mk, mk, [], U(write=5500, w1h=5500),
              expect("cold", U(write=7500, w1h=7500), [eop(CH, saved=h1(2000))]), facts={"ttl": "1h", "lifetime": "exact"})
    write_more("ttl/1h-entry-expired/cold-after-70-minutes/1h", "I56c",
               "r0 writes 5,000 tokens at 1h. r1 comes 70 minutes later, past the hour. The entry expired, so the phase is "
               "cold and the 2,000 replayed tokens are written at 1h: 2,000 x $6.00/M = $0.01200 (12,000 micro-dollars).",
               DIM("miss"), [s0, s1])

    # Row 4: r0 asks 1h, the provider bills 5m (CacheWrite1h = 0), and the host says so in Facts.TTL.
    s0 = step(0, "r0", body([], [], cm), body([], [], fm), marks("1h", 2), marks("1h", 2), [op], U(write=5000),
              expect("unknown", U(write=7000), [eop(CH, saved=TT(cacheWrite5m=2000), note="the provider billed the write at 5m")],
                     unpriced=[{"reason": "penalty_unknown"}]), facts={"ttl": "5m", "lifetime": "exact"})
    s1 = step(600, "r1", body([], [], c1m), body([], [], f1m), marks("1h", 3), marks("1h", 3), [], U(write=5500),
              expect("cold", U(write=7500), [eop(CH, saved=TT(cacheWrite5m=2000))]), facts={"ttl": "5m", "lifetime": "exact"})
    write_more("ttl/1h-ask-billed-5m/cold-after-10-minutes/5m", "I56d",
               "r0 asks for a 1h entry and the provider bills it as 5m (CacheWrite1h is 0); the host reports Facts.TTL 5m. "
               "The usage split alone is not enough: the library needs EntryFacts.TTL to agree. r1 comes 10 minutes later: "
               "the 5m entry expired, so the phase is cold and the 2,000 replayed tokens are written at 5m: "
               "2,000 x $3.75/M = $0.00750 (7,500 micro-dollars).", DIMS("rewrite", "afterBStarToL", "miss"), [s0, s1])

    # Row 5: a 1h head and a 2,000-token 5m tail, a ping at minute 4, the return at minute 8.
    KAW = "ka:main:w-i56e"
    m = [("a", 2000), ("b", 2000), ("c", 2000)]
    mk0 = [{"pos": P("messages", 1), "ttl": "1h"}, {"pos": P("messages", 2), "ttl": "5m"}]
    m1 = m + [("d", 500)]
    mk1 = [{"pos": P("messages", 1), "ttl": "1h"}, {"pos": P("messages", 3), "ttl": "5m"}]
    s0 = step(0, "r0", body([], [], m), body([], [], m), mk0, mk0, [], U(write=6000, w1h=4000),
              expect("unknown", U(write=6000, w1h=4000), [], unpriced=[{"reason": "penalty_unknown"}]), session=ses + "e",
              facts={"ttl": "5m", "lifetime": "exact"})
    facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(240), "pingRead": P("messages", 2), "pingWindow": KAW}
    s1 = step(480, "r1", body([], [], m1), body([], [], m1), mk1, mk1, [], U(read=6000, write=500),
              expect("hot", U(read=4000, write=2500), [], boundary=P("messages", 2),
                     effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=2000), caused=TT(cacheRead=2000), to=P("messages", 2))]),
              session=ses + "e", facts=facts)
    write_more("keepalive/ping-rescue-1h-head-5m-tail/tail-only/5m", "I56e",
               "A 1h head (4,000 tokens) and a 5m tail (2,000). A ping at minute 4 reads all 6,000. The request at minute 8 "
               "reads 6,000. Without the ping the 1h head was alive and the tail expired, so the rescue is the tail only: "
               "2,000 x ($3.75 - $0.30) = +$0.00690.",
               DIMS("rescue", "beforeBStar", "rescue", keepalive="true", ttl="mixed"),
               [s0, ping_step(240, ses + "e", KAW, ping_call(cacheRead=6000, input=5)), s1])

    m = [("a", 2000), ("b", 2000), ("c", 2000)]
    m1 = m + [("d", 500)]
    # Row 6: a 1h thread of 6,000, a ping at minute 59, the return at minute 62.
    KAW = "ka:main:w-i56f"
    mk0 = [{"pos": P("messages", 2), "ttl": "1h"}]
    mk1 = [{"pos": P("messages", 3), "ttl": "1h"}]
    s0 = step(0, "r0", body([], [], m), body([], [], m), mk0, mk0, [], U(write=6000, w1h=6000),
              expect("unknown", U(write=6000, w1h=6000), [], unpriced=[{"reason": "penalty_unknown"}]), session=ses + "f",
              facts={"ttl": "1h", "lifetime": "exact"})
    facts = {"ttl": "1h", "lifetime": "exact", "lastPing": ts(59 * 60), "pingRead": P("messages", 2), "pingWindow": KAW}
    s1 = step(62 * 60, "r1", body([], [], m1), body([], [], m1), mk1, mk1, [], U(read=6000, write=500, w1h=500),
              expect("hot", U(write=6500, w1h=6500), [], boundary=P("messages", 2),
                     effects=[effect("rescue", KAW, saved=TT(cacheWrite1h=6000), caused=TT(cacheRead=6000), to=P("messages", 2))]),
              session=ses + "f", facts=facts)
    write_more("keepalive/ping-rescue-1h-thread/across-the-hour/1h", "I56f",
               "A 1h thread of 6,000 tokens. A ping at minute 59 reads all 6,000 and renews the entry. The request at "
               "minute 62 is past the hour from r0 and 3 minutes after the ping. The rescue avoids a 1h write: "
               "6,000 x ($6.00 - $0.30) = +$0.03420.",
               DIMS("rescue", "beforeBStar", "rescue", keepalive="true", ttl="1h"),
               [s0, ping_step(59 * 60, ses + "f", KAW, ping_call(cacheRead=6000, input=5)), s1])


# accountant#55 rows 4 and 5: the split with both halves changed, and a mark that moves after a first-request miss.
def i55_split():
    SPL = "cg:cachesplit:i55a"
    msgs = [("m0", 2000), ("m1", 2000)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3000, 3000, SPL)
    bp = lambda last: BP(("system", 0), ("messages", last))
    # Row 4: the stable half and the volatile tail both change. The provider reads nothing of the system block.
    fsys0 = [("sysstable", 2500), ("sysvol", 500)]
    fsys1 = [("sysstable2", 2500), ("sysvol2", 500)]
    s0 = sim(0, "r0", ([], [("sysblk", 3000)], msgs), ([], fsys0, msgs), [], "unknown", add=[op],
             cbp=bp(1), fbp=bp(1))
    s1 = sim(30, "r1", ([], [("sysblk2", 3000)], msgs), ([], fsys1, msgs), [], "hot", add=[op], prev=(7000, 7000),
             usage=U(write=7000), base=U(write=7000), cbp=bp(1), fbp=bp(1),
             effects=[effect("break", "", note="the client changed its own system block; the break moves no tokens")])
    write_issue("split/both-halves-change/no-effect", 55,
                "cachesplit splits the system block into a stable half (2,500 tokens) and a volatile half (500). On the "
                "next request the stable half changes too, so the provider reads nothing and writes all 7,000. The client "
                "body would have done the same: the split saved nothing. No split effect. The break is the client's own "
                "and moves no tokens.",
                DIMS("setting-breakpoint", "system", "no-effect", hash="both-change"), [s0, s1])

    # Row 5: only a mark moves; the tail text is the same; the previous request was a first-request miss.
    m1 = [("m0", 2000), ("m1", 2000), ("m2", 500)]
    s0 = sim(0, "r0", ([], [("sysblk", 3000)], msgs), ([], fsys0, msgs), [], "unknown", add=[op],
             cbp=bp(1), fbp=bp(1))
    s1 = sim(30, "r1", ([], [("sysblk", 3000)], m1), ([], fsys0, m1), [], "hot", add=[op], prev=(7000, 7000),
             cbp=bp(2), fbp=bp(2), boundary=P("messages", 1))
    write_issue("split/mark-moves-after-first-request/no-effect", 55,
                "The first request (phase unknown) writes 7,000 tokens. On the second the client appends a message and "
                "moves its last mark from messages[1] to messages[2]. The system block and its tail are the same. The "
                "provider reads the 7,000 tokens of the first body. The client body would have read them too: no split "
                "effect.",
                DIMS("setting-breakpoint", "system", "no-effect", hash="same", mark="moved"), [s0, s1])


# accountant#55 row 3: a rewrite over media where the host gives Size.Tokens on one side only. The bill is below the
# media price (capped), so the range is not exact and the operation is unpriced (media_tokens, design §6.5 check 11).
def i55_media_one_side():
    SF = "cg:strip:i55m"
    C = [("m0", 500), ("img", 0, 2000000), ("m2", 2000)]
    F = [("m0", 500), ("img", 0, 1000000), ("m2", 2000)]
    op = rewrite("o1", "context-guru.strip", "messages", 1, 0, 0, SF)
    op["sizeBefore"] = {"bytes": 2000000, "mediaBytes": 2000000, "tokens": 1600}
    op["sizeAfter"] = {"bytes": 1000000, "mediaBytes": 1000000}
    s0 = media_step(0, "r0", C, F, op, billed=2350, exp_ops=[], checks=["media_tokens"],
                    unpriced=[{"opId": "o1", "reason": "media_tokens"}, {"reason": "penalty_unknown"}], baseline=2350)
    write_review("media/over-rewrite-tokens-one-side/unpriced", 55,
                 "The bill (2,350 tokens) is below the library price of the image (4,000), so the media is cut to the bill. "
                 "A rewrite over the image carries the host's count on the SizeBefore side only (1,600 tokens). The range "
                 "is exact only with both counts, so the operation is not priced (media_tokens). Only media_tokens fails.",
                 DIMS("rewrite", "media", "unknown", media="host-tokens-one-side"), [s0])


# accountant#55 row 6: Anthropic caches only at explicit marks. A body with none caches nothing: usage reads 0, all
# input. An operator cut at a new message saves input tokens at the input rate. No break is found.
def i55_no_cache():
    CF = "cg:cmdfilter:i55f"
    F = {"ttl": "5m", "lifetime": "exact", "minCacheable": 1000000}  # no mark: nothing reaches the minimum
    M = [(f"m{i}", 3000) for i in range(3)]
    s0 = sim(0, "r0", ([], SYS10, M), ([], SYS10, M), [], "unknown", cbp=[], fbp=[], usage=U(inp=11000), base=U(inp=11000),
             facts=F)
    c1, f1 = M + [("m3", 3000)], M + [("m3f", 600)]
    s1 = sim(40, "r1", ([], SYS10, c1), ([], SYS10, f1), [eop(CF, saved=TT(input=2400))], "hot", prev=(11000, 11000),
             cbp=[], fbp=[], add=[rewrite("o1", "context-guru.cmdfilter", "messages", 3, 3000, 600, CF)],
             usage=U(inp=11600), base=U(inp=14000), facts=F)
    write_issue("rewrite/no-cache-control/input/5m", 55,
                "The body has no cache_control and the provider needs explicit marks (Anthropic), so nothing is cached: "
                "usage reads 0 and bills everything as input, on both requests. An operator cuts message 3 from 3,000 to "
                "600 tokens. The saving is at the input rate: 2,400 x $3.00 = +$0.00720 (7,200 micro-dollars). No break "
                "is found, because there is no cached prefix to break.",
                DIMS("rewrite", "afterL", "none", breakpoints="none", provider="explicit-marks"), [s0, s1])


# accountant#14: the ping's extent is what it READ from the previous body. Fresh input in the ping (a
# sweep question, a summary request) is billed as Input but is not in that body, so it must not
# change the extent the ping covered.
def r14():
    for fresh in (0, 300):
        sysb, m0 = [("sys", 500)], [("m0", 1275), ("m1", 1276)]  # 3,051 tokens with the system block
        KAW = f"ka:main:w-r14-{fresh}"
        ses = f"s-r14-{fresh}"
        call = ping_call(cacheRead=3051, input=fresh)
        s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown", session=ses)
        m1 = m0 + [("m2", 300)]
        facts = {"ttl": "5m", "lifetime": "exact", "lastPing": ts(240), "pingRead": P("messages", 1), "pingWindow": KAW}
        s1 = sim(480, "r1", ([], sysb, m1), ([], sysb, m1), [], "hot", prev=(3051, 3051), facts=facts, session=ses,
                 usage=U(read=3051, write=300), base=U(write=3351), boundary=P("messages", 1),
                 effects=[effect("rescue", KAW, saved=TT(cacheWrite5m=3051), caused=TT(cacheRead=3051), to=P("messages", 1),
                                 note="the ping read the whole 3,051-token prefix")])
        write_review(f"keepalive/ping-rescue-ping-fresh-input-{fresh}/5m", 14,
                     f"A ping replays the 3,051-token prefix and reads all of it. It carries {fresh} tokens of fresh input "
                     f"(a sweep question). The question is billed as Input but is not in the previous body, so it does not "
                     f"change what the ping covered. The rescue is 3,051 x ($3.75 - $0.30) = +$0.01053 for any fresh input.",
                     DIMS("rescue", "beforeBStar", "rescue", keepalive="true", fresh=str(fresh)),
                     [s0, ping_step(240, ses, KAW, call), s1])


# accountant#21 and #20 share a cause: the host's body hash differs from the previous request's on a message the
# client did not change (it hashed a moving cache mark). The library then finds no common prefix.
# Usage shows what the provider read, and it is the evidence the library has.
def r21():
    sysb = [("sys", 500)]
    m0 = [("m0", 2750), ("m1", 2750)]  # 6,000 tokens with the system block
    SF = "cg:searchfold:r21a"
    s0 = sim(0, "r0", ([], sysb, m0), ([], sysb, m0), [], "unknown")
    # The client's last message is the same text with a different hash (the mark moved onto it).
    m1c = [("m0", 2750), ("m1x", 2750), ("m2", 2000)]
    m1f = [("m0", 2750), ("m1x", 2750), ("m2f", 500)]
    op = rewrite("o1", "context-guru.searchfold", "messages", 2, 2000, 500, SF)
    s1 = sim(30, "r1", ([], sysb, m1c), ([], sysb, m1f), [eop(SF, saved=TT(cacheWrite5m=1500))], "hot", add=[op],
             prev=(6000, 6000), usage=U(read=6000, write=500), base=U(read=6000, write=2000),
             cbp=bps(m1c), fbp=bps(m1f), boundary=P("messages", 1),
             effects=[effect("break", "", frm=P("messages", 1), to=P("messages", 1),
                             note="the hashes show a client difference at message 1, so a break with no ChangeID is on "
                                  "the record. The provider read all 6,000 tokens, so B* is the end of what it read and the "
                                  "break moves no token")])
    write_review("rewrite/prefix-hash-differs-usage-shows-read/hot/5m", 21,
                 "Turn 0 caches 6,000 tokens. On turn 1 message 1 has different bytes in the host's hash but the same text "
                 "(a cache mark moved onto it), so the two bodies share a prefix only up to message 0. The provider read "
                 "6,000 tokens, which is the whole of the previous body. searchfold cuts the new message 2 from 2,000 tokens to "
                 "500. Those tokens are after everything the provider read, so they are writes: 1,500 x $3.75 = +$0.00563. "
                 "The usage is the evidence for B*: the library must not put B* before what the provider read.",
                 DIMS("rewrite", "afterBStar", "hit", hash="differs"), [s0, s1])


# accountant#20, the plain form: nothing moved, and the previous request cached only part of the body. B* is
# the end of system, so the unchanged tail would have been read by the client body too. The split saved nothing.
def r20b():
    SPL = "cg:cachesplit:r20b"
    msgs = [("m0", 2000)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3000, 3000, SPL)
    fsys = [("sysstable", 2500), ("sysvol", 500)]
    cb, fb = BP(("system", 0)), BP(("system", 0))
    s0 = sim(0, "r0", ([], [("sysblk", 3000)], msgs), ([], fsys, msgs), [], "unknown", add=[op], cbp=cb, fbp=fb,
             usage=U(write=5000))
    s1 = sim(30, "r1", ([], [("sysblk", 3000)], msgs), ([], fsys, msgs), [], "hot", add=[op], prev=(5000, 5000),
             usage=U(read=5000), base=U(read=5000), cbp=cb, fbp=fb, boundary=P("system", 0))
    write_review("split/tail-unchanged/no-effect-same-request", 20,
                 "The same request is sent twice, 30 seconds apart. cachesplit has split the system block into a stable "
                 "half (2,500 tokens) and a volatile half (500). Nothing changed, so the client body, with its one "
                 "system block, would have read all 5,000 tokens too. The split saved nothing: no split effect.",
                 DIMS("setting-breakpoint", "system", "no-effect", hash="same"), [s0, s1])


def r20():
    SPL = "cg:cachesplit:r20a"
    msgs0 = [("m0", 2000), ("m1", 2000)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3000, 3000, SPL)
    fsys = [("sysstable", 2500), ("sysvol", 500)]
    s0 = sim(0, "r0", ([], [("sysblk", 3000)], msgs0), ([], fsys, msgs0), [], "unknown", add=[op],
             cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", 0), ("messages", 1)))
    msgs1 = [("m0", 2000), ("m1x", 2000)]  # same text, different hash: a mark moved
    s1 = sim(30, "r1", ([], [("sysblk", 3000)], msgs1), ([], fsys, msgs1), [], "hot", add=[op], prev=(7000, 7000),
             usage=U(read=7000), base=U(read=7000), boundary=P("messages", 1),
             cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", 0), ("messages", 1)),
             effects=[effect("break", "", frm=P("messages", 1), to=P("messages", 1),
                             note="a message the client did not change has a new hash; the provider read all 7,000 "
                                  "tokens, so B* is the end of what it read and no token moves")])
    write_review("split/tail-unchanged-hash-differs/no-effect", 20,
                 "cachesplit moved the breakpoint onto the stable half of the system block. The volatile tail does not "
                 "change between the two requests, so the client body would be read in full too: the split saved nothing. "
                 "The host's hash of message 1 differs on turn 1 (a cache mark moved onto it) although its text is the same, "
                 "and the provider read the whole 7,000 tokens. A split is credited only when the client body would have "
                 "written what the forwarded body read (design §4.4). Here it would not: no split effect, a zero total.",
                 DIMS("setting-breakpoint", "system", "no-effect", hash="differs"), [s0, s1])



# context-guru#909 guard: the s909 shape, but the system text did not change. The host moved a cache mark onto
# the system block, so the client hash and the volatile half's hash differ. The provider read the whole
# previous entry, so nothing broke and the client body would have read it in full too: no split effect.
def r909_system_hash():
    SPL = "cg:cachesplit:g909"
    m0 = [("m0", 20)]
    m1 = m0 + [("m1", 50)]
    op = rewrite("o1", "context-guru.cachesplit", "system", 0, 3150, 3150, SPL)
    bps = BP(("system", 0), ("messages", 0))
    s0 = sim(0, "r0", ([], [("sysblk", 3150)], m0), ([], [("sysstable", 3000), ("sysvol", 150)], m0), [],
             "unknown", add=[op], cbp=bps, fbp=bps)
    s1 = sim(40, "r1", ([], [("sysblk2", 3150)], m1), ([], [("sysstable", 3000), ("sysvol2", 150)], m1), [],
             "hot", add=[op], prev=(3170, 3170), usage=U(read=3170, write=50), base=U(read=3170, write=50),
             cbp=BP(("system", 0), ("messages", 1)), fbp=BP(("system", 0), ("messages", 1)),
             boundary=P("messages", 0),
             effects=[effect("break", "", frm=P("messages", 1), to=P("messages", 0),
                             note="the system block has a new hash; the provider read the whole 3,170 tokens, so B* "
                                  "is the end of what it read and no token moves")])
    write_issue("split/tail-unchanged-hash-differs-system/no-effect", "context-guru-909",
                "The s909 shape with one change: the system text is the same, but a cache mark moved onto the system "
                "block, so its hash and the hash of the volatile half differ. The entry is 3,170 tokens. The provider "
                "reads all 3,170 and writes the 50 new tokens. Nothing changed in content, so the client body, with one "
                "system block, would have read 3,170 too. The read passes the first forwarded difference, so the hash "
                "difference moved no content. No split effect: 3,170 x $0.30 + 50 x $3.75 on both sides, a zero total.",
                DIMS("setting-breakpoint", "system", "no-effect", hash="differs"), [s0, s1])

# ---------------------------------------------------------------------------------------------
# accountant#42 and #43: media in the ratio, and a long body with no breakpoint.
MEDIA_PER_TOKEN = 250  # the library price of base64 media: 1 token for each 250 bytes (design §4.2 rule 7)


def mbody(msgs):
    """A body of messages. A message is (id, text_tokens) or (id, text_tokens, media_bytes). A media
    position holds 4 bytes a text token plus media_bytes of base64 payload, all of it marked Media."""
    b = body([], [], [(m[0], m[1]) for m in msgs])
    for p, m in zip(b["pos"], msgs):
        if len(m) > 2:
            p["bytes"] += m[2]
            p["media"] = m[2]
    return b


def mtokens(msgs):
    return sum(m[1] + (m[2] // MEDIA_PER_TOKEN if len(m) > 2 else 0) for m in msgs)


def media_step(sec, rid, C, F, op, billed, exp_ops, checks=None, unpriced=None, baseline=None):
    """A first request (cold start: the boundary is unknown) over bodies with media. `billed` is the bill."""
    e = expect("unknown", U(write=baseline if baseline else mtokens(C)), exp_ops, unpriced=unpriced or [{"reason": "penalty_unknown"}], checks=checks)
    cbp = [{"pos": P("messages", len(C) - 1), "ttl": "5m"}]
    fbp = [{"pos": P("messages", len(F) - 1), "ttl": "5m"}]
    return step(sec, rid, mbody(C), mbody(F), cbp, fbp, [op], U(write=billed), e)


def c42():
    # A 250,000-byte image beside text. The image is 1,000 tokens at the library price and stays out of
    # the text ratio. Text: 1,000 + 2,000 tokens in the client body; the operator cuts m2 to 500.
    SF = "cg:cmdfilter:c42a"
    C = [("m0", 1000), ("img", 0, 250000), ("m2", 2000)]
    F = [("m0", 1000), ("img", 0, 250000), ("m2", 500)]
    op = rewrite("o1", "context-guru.cmdfilter", "messages", 2, 2000, 500, SF)
    s0 = media_step(0, "r0", C, F, op, billed=mtokens(F), exp_ops=[eop(SF, saved=TT(cacheWrite5m=1500))])
    write_review("media/large-image-beside-text/5m", 42,
                 "One request: a base64 image of 250,000 bytes (1,000 tokens at 1 token for 250 bytes) beside text. "
                 "The client body holds 1,000 + 2,000 text tokens and the image: 4,000 tokens. The operator cuts m2 from 2,000 "
                 "to 500 tokens. The forwarded body is billed 1,000 + 1,000 + 500 = 2,500 tokens. The text ratio leaves the "
                 "image out: (2,500 - 1,000) tokens over (4,000 + 2,000) forwarded text bytes = 1,500 / 6,000 = 0.25 tokens a byte. "
                 "The cut removes 6,000 bytes: 6,000 x 0.25 = 1,500 tokens saved, at the cache write tier. "
                 "If the image were counted in the ratio, 250,000 extra bytes would drag the ratio to 0.0098 and the saving "
                 "to 59 tokens.",
                 DIMS("rewrite", "media", "unknown", media="beside-text"), [s0])


def c42b():
    # A 2,000,000-byte image costs 8,000 tokens at the library price. The provider billed about 1,600
    # for it (downscaling). Text: 500 + 250 tokens forwarded. The bill is 2,350, below the price.
    SF = "cg:cmdfilter:c42b"
    C = [("m0", 500), ("img", 0, 2000000), ("m2", 2000)]
    F = [("m0", 500), ("img", 0, 2000000), ("m2", 250)]
    op = rewrite("o1", "context-guru.cmdfilter", "messages", 2, 2000, 250, SF)
    s0 = media_step(0, "r0", C, F, op, billed=2350, exp_ops=[eop(SF, saved=TT(cacheWrite5m=350))], checks=["media_tokens"],
                    baseline=2700)
    write_review("media/big-image-small-conversation/bill-below-media-price/5m", 42,
                 "One request: an image of 2,000,000 bytes, which the library prices at 8,000 tokens. The provider billed the "
                 "whole request 2,350 tokens (text 750 + image about 1,600), so the bill is below the library price. "
                 "The library cuts the media to the bill less the least the text can cost. Forwarded text: 2,000 + 1,000 = "
                 "3,000 bytes. The least is 0.05 x 3,000 = 150 tokens. The media gets 2,350 - 150 = 2,200 tokens. The text "
                 "ratio is 150 / 3,000 = 0.05. The operator cuts m2 from 8,000 to 1,000 bytes: 7,000 x 0.05 = 350 tokens "
                 "saved. This is the lowest figure the bill allows, so it is a floor. The check media_tokens fails to say "
                 "the media price was cut. The request is not dropped: before the fix nothing was priced "
                 "(unpriced ratio_range, saved 0). The baseline bill is 10,000 x 0.05 + 2,200 = 2,700 tokens: "
                 "2,700 - 2,350 = 350.",
                 DIMS("rewrite", "media", "unknown", media="bill-below-price"), [s0])


def c42c():
    # Second request of a thread, phase hot, capped ratio, one rewrite over an image, no Size.Tokens.
    # Conservation (check 6) must hold: the unpriced operation's tokens leave the bill difference.
    # The media/* cases above are first requests, where check 6 is skipped, so they could not show this.
    SF = "cg:strip:c42c"
    m0 = [("m0", 500)]
    C = [("m0", 500), ("img", 0, 2000000), ("m2", 2000)]
    F = [("m0", 500), ("img", 0, 1000000), ("m2", 2000)]
    op = rewrite("o1", "context-guru.strip", "messages", 1, 0, 0, SF)
    op["sizeBefore"] = {"bytes": 2000000, "mediaBytes": 2000000}
    op["sizeAfter"] = {"bytes": 1000000, "mediaBytes": 1000000}
    s0 = sim(0, "r0", ([], [], m0), ([], [], m0), [], "unknown", cbp=BP(("messages", 0)), fbp=BP(("messages", 0)))
    cbp, fbp = BP(("messages", 2)), BP(("messages", 2))
    e = expect("hot", U(inp=4200), [], unpriced=[{"opId": "o1", "reason": "media_tokens"}], checks=["media_tokens"],
               boundary=P("messages", 0))
    s1 = step(30, "r1", mbody(C), mbody(F), cbp, fbp, [op], U(inp=2350), e)
    write_review("media/big-image-hot-request-op-over-media/5m", 42,
                 "Request 0 caches one 500-token message. Request 1 (30 seconds later, hot) adds a 2,000,000-byte image and "
                 "an 8,000-byte message. A rewrite cuts the image to 1,000,000 bytes. The bill is 2,350 tokens, below the "
                 "library price of the forwarded image (1,000,000 / 250 = 4,000). Forwarded text = 2,000 + 8,000 = 10,000 bytes; "
                 "the least is 0.05 x 10,000 = 500 tokens; the media gets 2,350 - 500 = 1,850 tokens. The client media is "
                 "2,000,000 bytes = 3,700 tokens, so the bill difference is 500 + 3,700 - 2,350 = 1,850 tokens: all of it is the "
                 "image the rewrite removed. The rewrite covers media, so it is unpriced (media_tokens), and the check "
                 "conservation takes its 1,850 tokens out of the bill difference. Only media_tokens fails. "
                 "Before the fix conservation failed too.",
                 DIMS("rewrite", "media", "hot", media="hot-op-over-media"), [s0, s1])


def c42d():
    # Capped request, rewrite over an image, the host counted both sizes: priced, exact, not unpriced.
    SF = "cg:strip:c42d"
    C = [("m0", 500), ("img", 0, 2000000), ("m2", 2000)]
    F = [("m0", 500), ("img", 0, 1000000), ("m2", 2000)]
    op = rewrite("o1", "context-guru.strip", "messages", 1, 0, 0, SF)
    op["sizeBefore"] = {"bytes": 2000000, "mediaBytes": 2000000, "tokens": 1600}
    op["sizeAfter"] = {"bytes": 1000000, "mediaBytes": 1000000, "tokens": 800}
    exact = eop(SF, saved=TT(cacheWrite5m=800))
    del exact["estimated"]
    s0 = media_step(0, "r0", C, F, op, billed=2350, exp_ops=[exact], checks=["media_tokens"], baseline=3150)
    write_review("media/big-image-host-tokens-stay-exact/5m", 42,
                 "The bill (2,350 tokens) is below the library price of the image (4,000), so the media is cut to the bill. "
                 "A rewrite over the image carries the host's own counts: 1,600 tokens before and 800 after, both plausible. "
                 "The library uses them as they are: 1,600 - 800 = 800 tokens saved, not Estimated, and the operation is not "
                 "unpriced. The baseline bill is 2,350 + 800 = 3,150 tokens.",
                 DIMS("rewrite", "media", "unknown", media="host-tokens-capped"), [s0])


def c43():
    # 200 messages, no breakpoint at all (OpenAI-style traffic). extent must scan every position.
    n = 200
    msgs = [(f"m{i}", 100) for i in range(n)]
    SF = "cg:cmdfilter:c43a"
    m1c = msgs + [("m200", 400)]
    m1f = msgs + [("m200", 100)]
    op = rewrite("o1", "context-guru.cmdfilter", "messages", n, 400, 100, SF)
    s0 = sim(0, "r0", ([], [], msgs), ([], [], msgs), [], "unknown", cbp=[], fbp=[], usage=U(inp=20000), base=U(inp=20000))
    s1 = sim(30, "r1", ([], [], m1c), ([], [], m1f), [eop(SF, saved=TT(input=300))], "hot", add=[op],
             prev=(20000, 20000), usage=U(read=20000, inp=100), base=U(read=20000, inp=400), cbp=[], fbp=[],
             boundary=P("messages", n - 1))
    write_review("extent/no-breakpoint-long-body/hot/5m", 43,
                 "200 messages of 100 tokens, and no cache breakpoint at all (OpenAI-style traffic). Turn 0 caches 20,000 "
                 "tokens. Turn 1 adds a message of 400 tokens and the operator cuts it to 100. With no breakpoint the "
                 "library must search the whole body for the end of the cached prefix: every position is a candidate. "
                 "The provider read 20,000 tokens, the whole previous body, so B* is the last old message. The new "
                 "message is past B*, so its 300 saved tokens are fresh input (no breakpoint, so no cache write tier): 300 x $3.00 = +$0.0009. Before the fix the "
                 "search was quadratic in the number of positions; the result is the same.",
                 DIMS("rewrite", "afterBStar", "hit", breakpoints="none"), [s0, s1])


# accountant#44: input validation. A size above the whole client body, and a spend Call with a figure that
# cannot be real. Usage tiers above 2^40 and NaN/Inf have no case form: a case bills what its forwarded body
# holds, and JSON has no NaN. internal/engine/validation_inputs_test.go holds those.
def i44_size_range():
    sysb = [("sys", 2000)]
    m = [(f"m{i}", 3000) for i in range(4)]  # 14,000 tokens with the system block
    mc, mf = m + [("m4", 6000)], m + [("m4f", 1000)]
    big, ok = "cg:cmdfilter:i44a", "cg:cmdfilter:i44b"
    huge = 2_000_000_000_000  # tokens; 8e12 bytes, far above the 56,000-byte client body
    over = rewrite("o1", "context-guru.cmdfilter", "messages", 2, huge, huge, big)
    over["sizeBefore"]["tokens"] = huge
    good = rewrite("o2", "context-guru.cmdfilter", "messages", 4, 6000, 1000, ok)
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown")
    s1 = sim(40, "r1", ([], sysb, mc), ([], sysb, mf), [eop(ok, saved=TT(cacheWrite5m=5000))], "hot",
             add=[over, good], prev=(14000, 14000), boundary=P("messages", 3),
             unpriced=[{"opId": "o1", "reason": "size_range"}], checks=["size_range"])
    write_more("check/size-above-client-body/unpriced", "I44",
               "An operator reports that it received 2e12 tokens (8e12 bytes) and produced as many, but the client body "
               "of this request holds 80,000 bytes (20,000 tokens) and the forwarded body 60,000. Both sizes are above "
               "every bound of check 13, so the operation is not priced (size_range) and the check is reported. (Without "
               "the check this report would save 0 tokens; the 9.2e18-token saving of an unbounded SizeBefore is in the "
               "Go test.) A second, honest operation on the same request is priced as usual: message 4 shrinks "
               "from 6,000 to 1,000 tokens after B*, so 5,000 cache-write tokens are saved (5,000 x $3.75/M = $0.01875).",
               DIMS("rewrite", "afterBStar", "hit", check="size_range"), [s0, s1])


def i44_keeps_active():
    sysb = [("sys", 2000)]
    m = [(f"m{i}", 3000) for i in range(4)]
    mc, mf = m + [("m4", 6000)], m + [("m4f", 1000)]
    ch = "cg:cmdfilter:i44c"
    good = rewrite("o1", "context-guru.cmdfilter", "messages", 4, 6000, 1000, ch)
    bad = rewrite("o2", "context-guru.cmdfilter", "messages", 4, 2_000_000_000_000, 0, ch)
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown")
    s1 = sim(40, "r1", ([], sysb, mc), ([], sysb, mf), [eop(ch, saved=TT(cacheWrite5m=5000))], "hot",
             add=[good], prev=(14000, 14000), boundary=P("messages", 3))
    mc2, mf2 = mc + [("m5", 500)], mf + [("m5", 500)]
    s2 = sim(80, "r2", ([], sysb, mc2), ([], sysb, mf2), [eop(ch, saved=TT(cacheRead=5000), note="applied again")], "hot",
             add=[bad], prev=(20000, 15000), boundary=P("messages", 4),
             unpriced=[{"opId": "o2", "reason": "size_range"}], checks=["size_range"])
    write_more("check/size-above-client-body/keeps-active-change", "I44",
               "A change is reported once (request 1) and applied again later. On request 2 a bug reports the same "
               "ChangeID with a size of 2e12 tokens. The bad report is not priced (size_range) and it replaces "
               "nothing: the active change is still applied again, 5,000 tokens at the read tier (the client body "
               "would have been cached up to message 4): 5,000 x $0.30/M = $0.00150.",
               DIMS("rewrite", "beforeBStar", "hit", check="size_range", replay="true"), [s0, s1, s2])


def i44_after_insert():
    sysb = [("sys", 2000)]
    m = [("m0", 3000), ("m1", 3000)]
    ins = rewrite("o1", "context-guru.retrieve", "messages", 1, 0, 25000, "cg:retrieve:i44d",
                  splice={"at": 1, "removed": 0, "inserted": 1})
    # the downstream operator receives the 25,000-token insertion (100,000 bytes): above the client body
    # (32,000 bytes) but inside client + inserted (132,000). It cuts it to 5,000 tokens.
    down = rewrite("o2", "context-guru.cmdfilter", "messages", 1, 25000, 5000, "cg:cmdfilter:i44d")
    cf = m[:1] + [("ins", 5000)] + m[1:]
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, cf), [eop("cg:retrieve:i44d", caused=TT(cacheWrite5m=25000)),
                                                         eop("cg:cmdfilter:i44d", saved=TT(cacheWrite5m=20000))],
             "unknown", add=[ins, down], unpriced=[{"reason": "penalty_unknown"}])
    write_more("check/size-after-inserting-operator/priced", "I44",
               "A retrieval operator inserts 25,000 tokens (100,000 bytes) into a 32,000-byte client body. A cmdfilter after "
               "it receives that insertion and reports SizeBefore of 100,000 bytes: above the client body, but inside the "
               "bound of check 13 (client + inserted bytes = 132,000). The report stays priced: no size_range check fails. "
               "Retrieve caused 25,000 write tokens, cmdfilter saved 20,000: net -5,000 x $3.75/M = -$0.01875.",
               DIMS("rewrite", "mixed", "miss", operations="2", splice="true"), [s0], open_=[BSTAR_SNAP])


def i44_malformed_rereport():
    sysb = [("sys", 500)]
    m = [("m0", 500), ("m1", 500)]
    ins_c = "cg:retrieve:i44f"
    ins = rewrite("o1", "context-guru.retrieve", "messages", 1, 0, 25000, ins_c,
                  splice={"at": 1, "removed": 0, "inserted": 1})
    cf = m[:1] + [("ins", 25000)] + m[1:]
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, cf), [eop(ins_c, caused=TT(cacheWrite5m=25000))], "unknown",
             add=[ins], unpriced=[{"reason": "penalty_unknown"}])
    bad = rewrite("o2", "context-guru.retrieve", "messages", 1, 2_000_000_000_000, 25000, ins_c,
                  splice={"at": 1, "removed": 0, "inserted": 1})
    down = rewrite("o3", "context-guru.cmdfilter", "messages", 1, 25000, 5000, "cg:cmdfilter:i44f")
    cf2 = m[:1] + [("ins2", 5000)] + m[1:]
    s1 = sim(1500, "r1", ([], sysb, m), ([], sysb, cf2), [eop("cg:cmdfilter:i44f", saved=TT(cacheWrite5m=20000)),
                                                          eop(ins_c, caused=TT(cacheWrite5m=25000))], "cold",
             add=[bad, down], unpriced=[{"opId": "o2", "reason": "size_range"}], checks=["size_range"])
    write_more("check/malformed-rereport-keeps-insertion/priced", "I44",
               "Request 0 stores an active insertion of 25,000 tokens. Request 1 reports the same ChangeID again with an "
               "impossible SizeBefore, and an honest cmdfilter that cuts the insertion from 25,000 to 5,000 tokens. The "
               "bad re-report replaces nothing, so the insertion still counts in the bound of check 13 and the honest "
               "operation stays priced: it saves 20,000 write tokens and the insertion causes 25,000 (net -5,000 x $3.75/M = -$0.01875).",
               DIMS("rewrite", "mixed", "miss", check="size_range", splice="true"), [s0, s1], open_=[BSTAR_SNAP])


def i44_honest_chain():
    sysb = [("sys", 500)]
    m = [("m0", 4000), ("m1", 4000)]  # 8,500 tokens with the system block
    ci_c = "cg:retrieve:i44g"
    ci = rewrite("o1", "context-guru.retrieve", "messages", 1, 0, 25000, ci_c,
                 splice={"at": 1, "removed": 0, "inserted": 1})
    cf = m[:1] + [("ins", 25000)] + m[1:]
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, cf), [eop(ci_c, caused=TT(cacheWrite5m=25000))], "unknown",
             add=[ci], unpriced=[{"reason": "penalty_unknown"}])
    bad = rewrite("bad", "context-guru.retrieve", "messages", 1, 2_000_000_000_000, 25000, ci_c,
                  splice={"at": 1, "removed": 0, "inserted": 1})
    v = rewrite("v", "context-guru.more", "messages", 1, 0, 22500, "cg:more:i44g",
                splice={"at": 1, "removed": 0, "inserted": 1})
    d = rewrite("d", "context-guru.cmdfilter", "messages", 1, 55500, 18000, "cg:cmdfilter:i44g")
    cf2 = [("x", 18000)]
    s1 = sim(1500, "r1", ([], sysb, m), ([], sysb, cf2), [eop("cg:more:i44g", caused=TT(cacheWrite5m=22500)),
                                                         eop("cg:cmdfilter:i44g", saved=TT(cacheWrite5m=37500)),
                                                         eop(ci_c, caused=TT(cacheWrite5m=25000))], "cold",
             add=[bad, v, d], unpriced=[{"opId": "bad", "reason": "size_range"}], checks=["size_range"])
    write_more("check/honest-chain-beside-malformed-rereport/priced", "I44",
               "Client body 34,000 bytes (8,500 tokens). An active insertion of 25,000 tokens (100,000 bytes). On "
               "request 1 a bug re-reports its ChangeID with an impossible SizeBefore, and two honest operations ride "
               "beside it: v inserts 22,500 tokens, d cuts 55,500 tokens to 18,000. The forwarded body is 18,000 tokens. "
               "Only the bad re-report is size_range. v and d stay priced: v causes 22,500 write tokens, d saves 37,500, "
               "the active insertion causes 25,000: net -10,000 x $3.75/M = -$0.0375.",
               DIMS("rewrite", "mixed", "miss", check="size_range", splice="true"), [s0, s1], open_=[BSTAR_SNAP])


def i44_bad_call(name, field, value, text):
    ses = f"s-i44-{name}"
    call = spend_call(6000, 400, 0.008)
    call["usage"][field] = value
    s = ping_step(10, ses, "cg:extract_llm:i44", call)
    s["expected"]["ops"] = [eop("cg:extract_llm:i44", call=call)]
    s["expected"]["unpriced"] = [{"opId": "op1", "reason": "bad_usage"}]
    s["expected"]["checks"] = ["bad_usage"]
    write_more(f"check/bad-call-{name}/unpriced", "I44", text, DIMS("spend", "none", "none", check="bad_usage"), [s])


def issue54(before, after, reason):
    """accountant#54: the SizeBefore bound of check 13, at the limit and below zero. reason "" means priced."""
    sysb = [("sys", 2000)]
    m = [(f"m{i}", 3000) for i in range(4)]
    mc, mf = m + [("m4", 6000)], m + [("m4f", 1000)]  # client body 20,000 tokens = 80,000 bytes; forwarded 60,000
    s0 = sim(0, "r0", ([], sysb, m), ([], sysb, m), [], "unknown")
    if not reason:  # the one operation reports the whole client body in and the whole forwarded body out
        ch = "cg:cmdfilter:i54"
        op = rewrite("o1", "context-guru.cmdfilter", "messages", 4, 0, 0, ch)
        op["sizeBefore"], op["sizeAfter"] = {"bytes": before}, {"bytes": after}
        s1 = sim(40, "r1", ([], sysb, mc), ([], sysb, mf), [eop(ch, saved=TT(cacheWrite5m=5000))], "hot",
                 add=[op], prev=(14000, 14000), boundary=P("messages", 3))
        return [s0, s1]
    bad = rewrite("o1", "context-guru.cmdfilter", "messages", 2, 0, 0, "cg:cmdfilter:i54bad")
    bad["sizeBefore"], bad["sizeAfter"] = {"bytes": before}, {"bytes": after}
    ok = "cg:cmdfilter:i54ok"
    good = rewrite("o2", "context-guru.cmdfilter", "messages", 4, 6000, 1000, ok)
    s1 = sim(40, "r1", ([], sysb, mc), ([], sysb, mf), [eop(ok, saved=TT(cacheWrite5m=5000))], "hot",
             add=[bad, good], prev=(14000, 14000), boundary=P("messages", 3),
             unpriced=[{"opId": "o1", "reason": reason}], checks=[reason] if reason == "size_range" else None)
    return [s0, s1]


I54_BODY = ("The client body holds 20,000 tokens = 80,000 bytes and the forwarded body 15,000 tokens = 60,000 bytes. ")


def i54():
    write_more("check/size-before-at-client-body/priced", "I54",
               I54_BODY + "One operator reports SizeBefore of exactly 80,000 bytes (the whole client body) and SizeAfter "
               "of 60,000 (the whole forwarded body). The bound is inclusive: the operation is priced and no check "
               "fails. It saves 5,000 write tokens (5,000 x $3.75/M = $0.01875), estimated because the sizes are bytes.",
               DIMS("rewrite", "afterBStar", "hit", check="size_range"), issue54(80000, 60000, ""))
    write_more("check/size-before-one-byte-above-client-body/unpriced", "I54",
               I54_BODY + "An operator reports SizeBefore of 80,001 bytes, one byte above the client body. It is not "
               "priced (size_range) and the check is reported. A second, honest operation on the same request is "
               "priced as usual: 5,000 x $3.75/M = $0.01875.",
               DIMS("rewrite", "afterBStar", "hit", check="size_range"), issue54(80001, 0, "size_range"))
    write_more("check/size-before-negative/unpriced", "I54",
               I54_BODY + "An operator reports SizeBefore of -4 bytes. A negative size is its own failure: the "
               "operation is not priced (negative_size) and it never reaches the range bound of check 13. No check is reported: the reason is on Unpriced (design §6.5). "
               "The honest operation beside it is priced as usual.",
               DIMS("rewrite", "afterBStar", "hit", check="negative_size"), issue54(-4, 0, "negative_size"))
    write_more("check/size-after-negative/unpriced", "I54",
               I54_BODY + "An operator reports SizeAfter of -4 bytes. The operation is not priced (negative_size) and no check is reported. "
               "The honest operation beside it is priced as usual.",
               DIMS("rewrite", "afterBStar", "hit", check="negative_size"), issue54(4000, -4, "negative_size"))


def i44():
    i44_size_range()
    i44_keeps_active()
    i44_after_insert()
    i44_malformed_rereport()
    i44_honest_chain()
    i44_bad_call("negative-output", "output", -5,
                 "A model call states -5 output tokens. No response can bill that. The spend goes to Unpriced "
                 "(bad_usage) and the check bad_usage fails. The host must not add a negative cost to its totals.")
    i44_bad_call("negative-cost", "costUSD", -0.008,
                 "A model call states a cost of -$0.008. The spend goes to Unpriced (bad_usage) and the check "
                 "bad_usage fails. A valid call (case spend/without-rewrite/refused) is unchanged.")

# Issue 49: a breakpoint the host adds after the client's last one. The provider writes the tokens between the two at
# the write tier. The client body bills them as input. The mark costs 0.25 x n (5m) or 1.0 x n (1h) in input units.
MP = "cg:tailbreakpoint:a49"


# accountant#52: the turn after a host-added cache mark. Turn 0: the client marks messages[0] (5,000 tokens), the
# host adds a mark at messages[2], and the provider writes 11,000 (11,000 tokens: 5,000 + 3,000 + 3,000). The client
# body would write 5,000 and bill 6,000 as input (the markPremium of turn 0). Turn 1 keeps the client mark and the
# host adds none; the markPayback is what the provider reads of the host's tail.
I52_NOTE = "the client body bills these tokens as input; the host's mark makes the provider write them"


def i52_r0(ttl, ses=None):
    one_h = ttl == "1h"
    m0 = [("m0", 5000), ("m1", 3000), ("m2", 3000)]
    cb, fb = BP(("messages", 0), ttl=ttl), BP(("messages", 0), ("messages", 2), ttl=ttl)
    wr = dict(write=11000, w1h=11000) if one_h else dict(write=11000)
    base = dict(write=5000, inp=6000, w1h=5000) if one_h else dict(write=5000, inp=6000)
    tier = TT(cacheWrite1h=6000) if one_h else TT(cacheWrite5m=6000)
    kw = {"session": ses} if ses else {}
    return sim(0, "r0", ([], [], m0), ([], [], m0), [], "unknown", cbp=cb, fbp=fb, usage=U(**wr), base=U(**base),
               facts={"ttl": ttl, "lifetime": "exact"},
               effects=[effect("markPremium", "", saved=TT(input=6000), caused=tier, frm=P("messages", 0),
                               to=P("messages", 2), ttl=ttl, note=I52_NOTE)], **kw), m0, cb


def i52_all():
    # Row 1: 5m, the next turn at 40 s. Payback +$0.01620.
    s0, m0, cb = i52_r0("5m")
    m1 = m0 + [("m3", 500)]
    s1 = sim(40, "r1", ([], [], m1), ([], [], m1), [], "hot", prev=(11000, 11000), cbp=cb, fbp=cb,
             usage=U(read=11000, write=500), base=U(read=5000, write=500, inp=6000),
             effects=[effect("markPayback", "", saved=TT(input=6000), caused=TT(cacheRead=6000), frm=P("messages", 0),
                             to=P("messages", 2), note="the client body bills these tokens as input; the provider reads them")])
    write_issue("markpayback/next-turn/hot/5m", 52,
                "Turn 0 has a client mark at messages[0] (5,000 tokens) and a host mark at messages[2]. The provider "
                "writes 11,000, and the host mark costs 6,000 x ($3.75 - $3.00) = -$0.00450. Turn 1, 40 seconds later, "
                "keeps the client mark and adds no host mark. The provider reads 11,000. The client body would read 5,000 "
                "and bill the other 6,000 as input, so the host mark pays back 6,000 x ($3.00 - $0.30) = +$0.01620.",
                DIMS("setting-breakpoint", "messages", "mark-payback"), [s0, s1])

    # Row 2: all 1h. Premium -$0.01800, payback +$0.01620: net -$0.00180 over two turns, positive from turn 3.
    s0, m0, cb = i52_r0("1h")
    s1 = sim(40, "r1", ([], [], m1), ([], [], m1), [], "hot", prev=(11000, 11000), cbp=cb, fbp=cb,
             usage=U(read=11000, write=500, w1h=500), base=U(read=5000, write=500, inp=6000, w1h=500),
             facts={"ttl": "1h", "lifetime": "exact"},
             effects=[effect("markPayback", "", saved=TT(input=6000), caused=TT(cacheRead=6000), frm=P("messages", 0),
                             to=P("messages", 2), note="the client body bills these tokens as input; the provider reads them")])
    write_issue("markpayback/next-turn/hot/1h", 52,
                "The same two turns with every mark at 1h. The premium on turn 0 is 6,000 x ($6.00 - $3.00) = -$0.01800. "
                "The payback on turn 1 is the same +$0.01620 (the read rate does not depend on the TTL). Net over two "
                "turns: -$0.00180. Each later turn pays back +$0.01620 again, so the mark breaks even at turn 3.",
                DIMS("setting-breakpoint", "messages", "mark-payback", ttl="1h"), [s0, s1])

    # Row 3: past the 5m TTL: the entry expired. No credit.
    s0, m0, cb = i52_r0("5m")
    s1 = sim(360, "r1", ([], [], m1), ([], [], m1), [], "cold", cbp=cb, fbp=cb, usage=U(write=11500))
    write_issue("markpayback/next-turn-after-expiry/cold/5m", 52,
                "Turn 1 comes 6 minutes after turn 0, past the 5m TTL, with the client mark only. The entry expired, so the "
                "provider writes all 11,500 and the client body would have done the same. There is no payback, and "
                "the premium of turn 0 (-$0.00450) stays as a cost.",
                DIMS("setting-breakpoint", "messages", "mark-payback-expired"), [s0, s1])

    # Row 4: the provider cannot find the host entry (lookback). It reads the client mark's 5,000 only.
    s0, m0, cb = i52_r0("5m")
    s1 = sim(40, "r1", ([], [], m1), ([], [], m1), [], "hot", prev=(11000, 11000), cbp=cb, fbp=cb,
             usage=U(read=5000, write=6500), base=U(read=5000, write=6500))
    write_issue("markpayback/host-entry-not-found/hot/5m", 52,
                "Turn 1 adds more blocks than the provider looks back from its mark (20), so it cannot find the host's "
                "entry. It reads the client mark's 5,000 and writes the other 6,500 again. The client body would do "
                "the same. No payback: only the read the client body also had.",
                DIMS("setting-breakpoint", "messages", "mark-payback-missed"), [s0, s1])


def c49():
    m0 = [("m0", 5000)]
    m3 = m0 + [("m1", 3000), ("m2", 3000)]
    cb0, cb3 = BP(("messages", 0)), BP(("messages", 0))
    fb3 = BP(("messages", 0), ("messages", 2))
    note = "the client body bills these tokens as input; the host's mark makes the provider write them"

    # hot, 5m
    s0 = sim(0, "r0", ([], [], m0), ([], [], m0), [], "unknown", cbp=cb0, fbp=cb0, usage=U(write=5000))
    s1 = sim(10, "r1", ([], [], m3), ([], [], m3), [], "hot", prev=(5000, 5000), cbp=cb3, fbp=fb3,
             usage=U(read=5000, write=6000), base=U(read=5000, inp=6000),
             effects=[effect("markPremium", "", saved=TT(input=6000), caused=TT(cacheWrite5m=6000), frm=P("messages", 0), to=P("messages", 2),
                             ttl="5m", note=note)])
    write_issue("markpremium/added-tail/hot/5m", 49,
                "The client has one breakpoint, at messages[0]. The host adds one at the last message. The provider reads "
                "the 5,000 tokens of messages[0] and writes the 6,000 tokens after it at the 5m rate. The client body "
                "would bill them as input. The mark costs 6,000 x ($3.75 - $3.00) = -$0.00450 (markPremium). The host "
                "reports no operation for the mark itself, so the effect has no ChangeID. The next request reads what "
                "this one wrote: that gain is priced on that request, not here.",
                DIMS("setting-breakpoint", "messages", "mark-premium"), [s0, s1])

    # cold: the first request. The client body writes m0 and bills the rest as input.
    s0 = sim(0, "r0", ([], [], m3), ([], [], m3), [], "unknown", cbp=cb3, fbp=fb3, usage=U(write=11000),
             base=U(write=5000, inp=6000),
             effects=[effect("markPremium", "", saved=TT(input=6000), caused=TT(cacheWrite5m=6000), frm=P("messages", 0), to=P("messages", 2),
                             ttl="5m", note=note)])
    write_issue("markpremium/added-tail/first-request/5m", 49,
                "The same marks on a first request (phase unknown, nothing cached). The client body would write messages[0] (5,000 tokens) under its "
                "own mark and bill the other 6,000 as input. The forwarded body writes all 11,000. The difference is "
                "the same: 6,000 x ($3.75 - $3.00) = -$0.00450. A cold request does not remove the premium, because "
                "the client body also leaves the tail as input.",
                DIMS("setting-breakpoint", "messages", "mark-premium"), [s0])

    # hot, 1h
    cb1h = BP(("messages", 0), ttl="1h")
    fb1h = BP(("messages", 0), ("messages", 2), ttl="1h")
    f1h = {"ttl": "1h", "lifetime": "exact"}
    s0 = sim(0, "r0", ([], [], m0), ([], [], m0), [], "unknown", cbp=cb1h, fbp=cb1h, usage=U(write=5000, w1h=5000), facts=f1h,
             base=U(write=5000, w1h=5000))
    s1 = sim(10, "r1", ([], [], m3), ([], [], m3), [], "hot", prev=(5000, 5000), cbp=cb1h, fbp=fb1h, facts=f1h,
             usage=U(read=5000, write=6000, w1h=6000), base=U(read=5000, inp=6000),
             effects=[effect("markPremium", "", saved=TT(input=6000), caused=TT(cacheWrite1h=6000), frm=P("messages", 0), to=P("messages", 2),
                             ttl="1h", note=note)])
    write_issue("markpremium/added-tail/hot/1h", 49,
                "The same host mark with 1h breakpoints. The provider writes the 6,000 tail tokens at the 1h rate. The "
                "client body bills them as input: 6,000 x ($6.00 - $3.00) = -$0.01800.",
                DIMS("setting-breakpoint", "messages", "mark-premium", ttl="1h"), [s0, s1])

    # no write: the same body again, the provider reads both marks
    s0 = sim(0, "r0", ([], [], m3), ([], [], m3), [], "unknown", cbp=cb3, fbp=fb3, usage=U(write=11000),
             base=U(write=5000, inp=6000),
             effects=[effect("markPremium", "", saved=TT(input=6000), caused=TT(cacheWrite5m=6000), frm=P("messages", 0), to=P("messages", 2),
                             ttl="5m", note=note)])
    s1 = sim(10, "r1", ([], [], m3), ([], [], m3), [], "hot", prev=(11000, 11000), cbp=cb3, fbp=fb3,
             usage=U(read=11000), base=U(read=5000, inp=6000),
             effects=[effect("markPayback", "", saved=TT(input=6000), caused=TT(cacheRead=6000), frm=P("messages", 0),
                             to=P("messages", 2), note="the client body bills these 6,000 tokens as input; the host's mark "
                                                       "made the provider write them on request 0, and it reads them now")])
    write_issue("markpayback/inside-previous-read/hot/5m", 49,
                "The request repeats a body whose host mark the previous request wrote. The provider reads all 11,000 "
                "tokens and writes none. The client body has one mark, at messages[0]: it would read 5,000 and bill the "
                "other 6,000 as input. No write, so no markPremium. The mark pays back what request 0 paid: "
                "6,000 x ($3.00 - $0.30) = +$0.01620 (accountant#52).",
                DIMS("setting-breakpoint", "messages", "mark-payback"), [s0, s1])

    # the client mark is already at the end: nothing to add
    s0 = sim(0, "r0", ([], [], m3), ([], [], m3), [], "unknown", cbp=BP(("messages", 2)), fbp=BP(("messages", 2)),
             usage=U(write=11000))
    write_issue("markpremium/host-mark-is-client-mark/cold/5m", 49,
                "The host adds no breakpoint: the forwarded marks equal the client's. No markPremium.",
                DIMS("setting-breakpoint", "messages", "mark-premium-none"), [s0])

    # a host operation in the same request: a +4 token rewrite at the new tail
    m3b = m0 + [("m1", 3000), ("m2b", 3004)]
    op = rewrite("o1", "context-guru.tailbreakpoint", "messages", 2, 3000, 3004, MP)
    s0 = sim(0, "r0", ([], [], m0), ([], [], m0), [], "unknown", cbp=cb0, fbp=cb0, usage=U(write=5000))
    s1 = sim(10, "r1", ([], [], m3), ([], [], m3b), [], "hot", prev=(5000, 5000), add=[op], cbp=cb3, fbp=fb3,
             usage=U(read=5000, write=6004), base=U(read=5000, inp=6000),
             effects=[effect("markPremium", MP, saved=TT(input=6000), caused=TT(cacheWrite5m=6000), frm=P("messages", 0), to=P("messages", 2),
                             ttl="5m", note=note)])
    s1["expected"]["ops"] = [eop(MP, caused=TT(cacheWrite5m=4), note="the 4 bytes-tokens the mark's text adds, at the write rate")]
    write_issue("markpremium/with-mark-text/hot/5m", 49,
                "The host reports a rewrite for the mark's own text (+4 tokens) and adds the breakpoint. The operation "
                "prices its 4 tokens at the write rate (-$0.00001500). The markPremium effect prices the 6,000 tokens "
                "that both bodies hold, and carries the host's ChangeID: 6,000 x $0.75 = -$0.00450. The two do not "
                "count the same token twice.",
                DIMS("setting-breakpoint", "messages", "mark-premium"), [s0, s1])

    # headttl plus a host 1h tail mark: each token is booked once
    sysb, ms = [("sys", 1000)], [("m0", 1000), ("m1", 1000), ("m2", 1000)]
    HT = "headttl:main:sysa49"
    hop = rewrite("o1", "context-guru.headttl", "system", 0, 1000, 1000, HT)
    cbp = [{"pos": P("system", 0), "ttl": "5m"}, {"pos": P("messages", 0), "ttl": "5m"}]
    fbp = [{"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 0), "ttl": "5m"}, {"pos": P("messages", 2), "ttl": "1h"}]
    s0 = sim(0, "r0", ([], sysb, ms), ([], sysb, ms), [], "unknown", add=[hop], cbp=cbp, fbp=fbp,
             usage=U(write=4000, w1h=3000), base=U(write=2000, inp=2000),
             effects=[effect("ttlPremium", HT, saved=TT(cacheWrite5m=1000), caused=TT(cacheWrite1h=1000), ttl="1h",
                             note="the head only"),
                      effect("markPremium", "", saved=TT(input=2000), caused=TT(cacheWrite1h=2000), frm=P("messages", 0),
                             to=P("messages", 2), ttl="1h", note=note)],
             facts={"ttl": "1h", "lifetime": "exact"})
    write_issue("markpremium/with-headttl/first-request/1h", 49,
                "headttl holds the 1,000-token system head at 1h and the host adds a 1h mark at the last message. The "
                "client body writes sys and m0 (2,000, 5m) and bills m1 and m2 as input. ttlPremium books the head "
                "only: 1,000 x ($6.00 - $3.75) = -$0.00225. markPremium books the 2,000 tail tokens: "
                "2,000 x ($6.00 - $3.00) = -$0.00600. No token is booked twice. Total -$0.00825 equals the bill "
                "difference.",
                DIMS("setting-breakpoint", "messages", "mark-premium-headttl", ttl="1h"), [s0])

    # mixed TTLs inside the range, both orders. The client marks tools[0].
    tools1, sys1, ms1 = [("t", 1000)], [("sys", 1000)], [("m0", 1000), ("m1", 1000)]
    ctb = [{"pos": P("tools", 0), "ttl": "5m"}]
    for name, fb, u, caused, cost in (
            ("1h-inside-5m-last", [("tools", 0, "5m"), ("system", 0, "1h"), ("messages", 1, "5m")], U(write=4000, w1h=1000),
             TT(cacheWrite5m=2000, cacheWrite1h=1000), "1,000 x ($6.00 - $3.00) + 2,000 x ($3.75 - $3.00) = -$0.00450"),
            ("5m-inside-1h-last", [("tools", 0, "5m"), ("system", 0, "5m"), ("messages", 1, "1h")], U(write=4000, w1h=2000),
             TT(cacheWrite5m=1000, cacheWrite1h=2000), "1,000 x ($3.75 - $3.00) + 2,000 x ($6.00 - $3.00) = -$0.00675")):
        fbp2 = [{"pos": P(sec, i), "ttl": t} for sec, i, t in fb]
        s0 = sim(0, "r0", (tools1, sys1, ms1), (tools1, sys1, ms1), [], "unknown", cbp=ctb, fbp=fbp2, usage=u,
                 base=U(write=1000, inp=3000),
                 effects=[effect("markPremium", "", saved=TT(input=3000), caused=caused, frm=P("tools", 0),
                                 to=P("messages", 1), ttl="1h", note=note)])
        write_issue(f"markpremium/mixed-ttl/{name}/5m", 49,
                    "The client marks tools[0]. The host adds a mark at system[0] and one at the last message, with "
                    "different TTLs. The provider writes each part at the TTL of the first mark at or after it (design "
                    f"6.2). The 3,000 tokens after the client's mark move from input to those tiers: {cost}.",
                    DIMS("setting-breakpoint", "messages", "mark-premium-mixed"), [s0])

    # headttl + host 1h mark + an operation that adds 500 tokens inside the range
    HT2 = "headttl:main:sysb49"
    ms_f = [("m0", 1000), ("m1", 1500), ("m2", 1000)]
    aop = rewrite("o2", "context-guru.expand", "messages", 1, 1000, 1500, "cg:expand:b49")
    s0 = sim(0, "r0", ([], sysb, ms), ([], sysb, ms_f), [], "unknown", add=[rewrite("o1", "context-guru.headttl", "system", 0, 1000, 1000, HT2), aop],
             cbp=cbp, fbp=fbp, usage=U(write=4500, w1h=3500), base=U(write=2000, inp=2000),
             effects=[effect("ttlPremium", HT2, saved=TT(cacheWrite5m=1000), caused=TT(cacheWrite1h=1000), ttl="1h",
                             note="the head only: the added tokens are not a 5m write moved to 1h"),
                      effect("markPremium", "", saved=TT(input=2000), caused=TT(cacheWrite1h=2000), frm=P("messages", 0),
                             to=P("messages", 2), ttl="1h", note=note)],
             facts={"ttl": "1h", "lifetime": "exact"})
    s0["expected"]["ops"] = [eop("cg:expand:b49", caused=TT(cacheWrite1h=500),
                                 note="the added 500 tokens are written at 1h; the client body has none of them")]
    write_issue("markpremium/with-headttl-and-adding-op/first-request/1h", 49,
                "headttl, a host 1h mark and an operation that adds 500 tokens inside the marked range. ttlPremium books "
                "the head only: 1,000 x $2.25/M = -$0.00225. markPremium books the 2,000 client tokens: -$0.00600. The "
                "operation books its 500 tokens at 1h: -$0.00300. The total -$0.01125 equals the bill difference "
                "(4,500 to 8,250 units). No token is booked twice.",
                DIMS("setting-breakpoint", "messages", "mark-premium-headttl", ttl="1h"), [s0])

    # the client's last breakpoint is before the headttl head
    HT3 = "headttl:main:sysc49"
    tl, sl, ml = [("t", 1000)], [("sys", 1000)], [("m0", 1000), ("m1", 1000)]
    s0 = sim(0, "r0", (tl, sl, ml), (tl, sl, ml), [], "unknown",
             add=[rewrite("o1", "context-guru.headttl", "system", 0, 1000, 1000, HT3)],
             cbp=[{"pos": P("tools", 0), "ttl": "5m"}],
             fbp=[{"pos": P("tools", 0), "ttl": "5m"}, {"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 1), "ttl": "1h"}],
             usage=U(write=4000, w1h=3000), base=U(write=1000, inp=3000),
             effects=[effect("markPremium", "", saved=TT(input=3000), caused=TT(cacheWrite1h=3000), frm=P("tools", 0),
                             to=P("messages", 1), ttl="1h", note="the head is input in the client body: no ttlPremium")],
             facts={"ttl": "1h", "lifetime": "exact"})
    write_issue("markpremium/headttl-after-client-mark/first-request/1h", 49,
                "The client's last breakpoint (tools[0]) is before the headttl head. The client body bills the head "
                "as input, so the head's 1h write is input to 1h, and markPremium books it with the rest: 3,000 x "
                "($6.00 - $3.00)/M = -$0.00900. ttlPremium adds nothing, so the head is booked once.",
                DIMS("setting-breakpoint", "system", "mark-premium-headttl", ttl="1h"), [s0])

    # round 3, trace 1: a host 1h mark before the client's last breakpoint. The head is 1,000 and m0 is 2,000.
    HT4 = "headttl:main:sysd49"
    sd, md = [("sys", 1000)], [("m0", 2000), ("m1", 1000), ("m2", 1000)]
    s0 = sim(0, "r0", ([], sd, md), ([], sd, md), [], "unknown",
             add=[rewrite("o1", "context-guru.headttl", "system", 0, 1000, 1000, HT4)],
             cbp=BP(("system", 0), ("messages", 2)),
             fbp=[{"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 0), "ttl": "1h"}, {"pos": P("messages", 2), "ttl": "5m"}],
             usage=U(write=5000, w1h=3000), base=U(write=5000),
             effects=[effect("ttlPremium", HT4, saved=TT(cacheWrite5m=3000), caused=TT(cacheWrite1h=3000), frm="nil",
                             to=P("messages", 0), ttl="1h", note="the client body writes sys and m0 at 5m; the 1h marks write them at 1h")],
             facts={"ttl": "1h", "lifetime": "exact"})
    write_issue("markpremium/host-1h-mark-before-client-last/first-request/1h", 49,
                "headttl, a host 1h mark at messages[0], and the client's last mark at messages[2] (5m). The provider "
                "writes sys (1,000) and m0 (2,000) at 1h. The client body writes all 5,000 at 5m. Every token in sys and m0 "
                "moves from 5m to 1h: 3,000 x ($6.00 - $3.75)/M = -$0.00675. The head alone is 1,000, so a premium "
                "bounded to the head would book only -$0.00225.",
                DIMS("setting-breakpoint", "messages", "mark-premium-headttl", ttl="1h"), [s0])

    # round 3, trace 2: an operation adds 500 tokens before the head
    HT5 = "headttl:main:syse49"
    se, me = [("sys", 1000)], [("m0", 1000)]
    te = [("t", 500)]
    s0 = sim(0, "r0", ([], se, me), (te, se, me), [], "unknown",
             add=[rewrite("o1", "context-guru.headttl", "system", 0, 1000, 1000, HT5),
                  rewrite("o2", "context-guru.addtool", "tools", 0, 0, 500, "cg:addtool:e49")],
             cbp=BP(("system", 0), ("messages", 0)),
             fbp=[{"pos": P("system", 0), "ttl": "1h"}, {"pos": P("messages", 0), "ttl": "5m"}],
             usage=U(write=2500, w1h=1500), base=U(write=2000),
             effects=[effect("ttlPremium", HT5, saved=TT(cacheWrite5m=1000), caused=TT(cacheWrite1h=1000), frm=P("tools", 0),
                             to=P("system", 0), ttl="1h", note="the head only: the added tool is the operation's")])
    s0["expected"]["ops"] = [eop("cg:addtool:e49", caused=TT(cacheWrite1h=500),
                                 note="the added 500 tokens are written at 1h; the client body has none of them")]
    write_issue("markpremium/adding-op-before-head/first-request/1h", 49,
                "headttl and an operation that adds a 500-token tool before the head. The provider writes tools and sys "
                "at 1h (1,500). The client body writes sys and m0 at 5m (2,000). The operation books its 500 tokens at "
                "1h: -$0.00300. ttlPremium books the 1,000-token head only: -$0.00225. The total -$0.00525 equals the "
                "bill difference (2,500 to 4,250 units).",
                DIMS("setting-breakpoint", "system", "mark-premium-headttl", ttl="1h"), [s0])

    # round 4: the entry was evicted. The break is in the baseline, and the host's 1h mark still upgrades m0.
    me4 = [("m0", 1000), ("m1", 1000), ("m2", 1000), ("m3", 1000), ("m4", 1000)]
    s0 = sim(0, "r0", ([], [], me4), ([], [], me4), [], "unknown", cbp=BP(("messages", 4)), fbp=BP(("messages", 4)),
             usage=U(write=5000))
    s1 = sim(10, "r1", ([], [], me4), ([], [], me4), [], "hot", prev=(5000, 5000),
             cbp=BP(("messages", 1), ("messages", 4)),
             fbp=[{"pos": P("messages", 0), "ttl": "1h"}, {"pos": P("messages", 1), "ttl": "5m"}, {"pos": P("messages", 4), "ttl": "5m"}],
             usage=U(write=5000, w1h=1000), base=U(write=5000), facts={"ttl": "1h", "lifetime": "exact"},
             effects=[effect("break", "", saved=TT(cacheRead=5000), caused=TT(cacheWrite5m=5000), to=P("messages", 4),
                             note="the entry was lost early: the client body lost it too (in the baseline)"),
                      effect("ttlPremium", "", saved=TT(cacheWrite5m=1000), caused=TT(cacheWrite1h=1000), frm="nil",
                             to=P("messages", 0), ttl="1h",
                             note="the client body wrote m0 at 5m; the host's 1h mark writes it at 1h")])
    s1["expected"]["effects"] = [s1["expected"]["effects"][1], s1["expected"]["effects"][0]]
    write_issue("markpremium/host-1h-mark-on-evicted-entry/hot/1h", 49,
                "The previous request cached 5,000 tokens, and the provider evicted the entry: it reads nothing and "
                "writes all 5,000. The break is in the baseline: the client body lost the entry too and writes all 5,000 "
                "at 5m. The host's 1h mark at messages[0] writes m0 at 1h. ttlPremium books 1,000 x ($6.00 - $3.75)/M = "
                "-$0.00225. A premium that skipped the positions of an in-baseline break would book nothing and "
                "overstate the saving.",
                DIMS("setting-breakpoint", "messages", "mark-premium-evicted", ttl="1h"), [s0, s1])


# Issue 60: tool deferral with defer_loading. The host leaves each deferred definition in the body it forwards,
# and the provider bills a stub for it. The host builds the forwarded body as the prompt the provider bills, so
# a deferred tool has the size of its stub. The deferral is then a shrink of tools positions, priced with no
# new field.
def d60():
    TD = "td:set-1"
    tools_c = [("t0", 250), ("t1", 750), ("t2", 500)]
    tools_f = [("t0", 250), ("t1", 10), ("t2", 10)]  # t1 and t2 are deferred: the bill holds a stub of 10 tokens
    sysb = [("sys", 750)]
    op = rewrite("op1", "tool-defer", "tools", 1, 1250, 20, TD,
                 spans=[{"section": "tools", "from": 1, "to": 1, "sizeBefore": size(750), "sizeAfter": size(10)},
                        {"section": "tools", "from": 2, "to": 2, "sizeBefore": size(500), "sizeAfter": size(10)}])
    msgs = [("m0", 250), ("m1", 250)]
    plan = [(0, "r0", 1, "unknown", True), (60, "r1", 2, "hot", False), (1260, "r2", 2, "cold", False)]
    steps, prev_c, prev_f = [], 0, 0
    for sec, rid, n, phase, first in plan:
        m = msgs[:n]
        ct, ft = tokens(tools_c, sysb, m), tokens(tools_f, sysb, m)
        if phase == "hot":
            ops = [eop(TD, saved=TT(cacheRead=1230), note="tools come first, so the 1,230 tokens are before B*")]
        else:
            ops = [eop(TD, saved=TT(cacheWrite5m=1230), note="nothing is cached or the entry expired")]
        steps.append(sim(sec, rid, (tools_c, sysb, m), (tools_f, sysb, m), ops, phase,
                         add=[op] if first else [], prev=(prev_c, prev_f)))
        prev_c, prev_f = ct, ft
    write_issue("rewrite/tool-defer-stubs/first-hot-cold/5m", 60,
                "tool-defer sets defer_loading on 2 tools (750 + 500 = 1,250 tokens). The definitions stay in the "
                "body the host forwards, and the provider bills a stub of 10 tokens for each. The host builds the "
                "forwarded body as the billed prompt, so the 2 tools have 10 tokens each there, and the deferral "
                "saves 1,250 - 20 = 1,230 tokens. The tools come first in the prefix, so a hot request reads the "
                "saving ($0.00037) and a first or cold request writes it ($0.00461). The forwarded prefix "
                "does not change, so the deferral breaks nothing. The cold request has one break, the expired entry, and it "
                "moves no tokens.",
                DIMS("rewrite", "tools", "unknown-hit-miss", spans="true"), steps)


# Issue 60, 1h entry: the same deferral under 1h breakpoints. The 1h entry is still alive after the 5m TTL, so a
# request 20 minutes after the last one is hot, not cold. It becomes cold only after the 1h TTL.
def d60_1h():
    TD = "td:set-1h"
    tools_c = [("t0", 250), ("t1", 750), ("t2", 500)]
    tools_f = [("t0", 250), ("t1", 10), ("t2", 10)]
    sysb = [("sys", 750)]
    op = rewrite("op1", "tool-defer", "tools", 1, 1250, 20, TD,
                 spans=[{"section": "tools", "from": 1, "to": 1, "sizeBefore": size(750), "sizeAfter": size(10)},
                        {"section": "tools", "from": 2, "to": 2, "sizeBefore": size(500), "sizeAfter": size(10)}])
    msgs = [("m0", 250), ("m1", 250)]
    plan = [(0, "r0", 1, "unknown", True), (1260, "r1", 2, "hot", False), (6000, "r2", 2, "cold", False)]
    steps, prev_c, prev_f = [], 0, 0
    for sec, rid, n, phase, first in plan:
        m = msgs[:n]
        ct, ft = tokens(tools_c, sysb, m), tokens(tools_f, sysb, m)
        if phase == "hot":
            ops = [eop(TD, saved=TT(cacheRead=1230), note="tools come first, so the 1,230 tokens are before B*")]
            usage, base = U(read=prev_f, write=ft - prev_f, w1h=ft - prev_f), U(read=prev_c, write=ct - prev_c, w1h=ct - prev_c)
        else:
            ops = [eop(TD, saved=TT(cacheWrite1h=1230), note="nothing is cached or the entry expired")]
            usage, base = U(write=ft, w1h=ft), U(write=ct, w1h=ct)
        steps.append(sim(sec, rid, (tools_c, sysb, m), (tools_f, sysb, m), ops, phase,
                         add=[op] if first else [], prev=(prev_c, prev_f), cbp=bps(m, "1h"), fbp=bps(m, "1h"),
                         usage=usage, base=base, facts={"ttl": "1h", "lifetime": "exact"}))
        prev_c, prev_f = ct, ft
    write_issue("rewrite/tool-defer-stubs/first-hot-cold/1h", 60,
                "The same deferral as the 5m case, under 1h breakpoints. The second request comes 21 minutes after "
                "the first. A 5m entry would have expired, but the 1h entry is alive, so the request is hot and "
                "reads the saving. The third request comes more than 1h after the second, so the entry is cold and "
                "writes the saving at the 1h tier.",
                DIMS("rewrite", "tools", "unknown-hit-miss", ttl="1h", spans="true"), steps)


# Issue 60, loaded later: the model loads a deferred tool. Its full definition enters the messages. That is a
# rewrite that makes the context larger (R33). The host reports it as a cost with its own ChangeID. The deferral
# stays applied: the other deferred tool is still a stub.
def d60_loaded():
    TD, LD = "td:set-ld", "td:load-t1"
    tools_c = [("t0", 250), ("t1", 750), ("t2", 500)]
    tools_f = [("t0", 250), ("t1", 10), ("t2", 10)]
    sysb = [("sys", 750)]
    op = rewrite("op1", "tool-defer", "tools", 1, 1250, 20, TD,
                 spans=[{"section": "tools", "from": 1, "to": 1, "sizeBefore": size(750), "sizeAfter": size(10)},
                        {"section": "tools", "from": 2, "to": 2, "sizeBefore": size(500), "sizeAfter": size(10)}])
    load = rewrite("op2", "tool-defer", "messages", 2, 250, 1000, LD)
    m0, m1 = [("m0", 250), ("m1", 250)], [("m0", 250), ("m1", 250), ("m2", 250)]
    m1f = [("m0", 250), ("m1", 250), ("m2f", 1000)]  # the loaded definition (750 tokens) is in the new message
    s0 = sim(0, "r0", (tools_c, sysb, m0[:1]), (tools_f, sysb, m0[:1]), [eop(TD, saved=TT(cacheWrite5m=1230))],
             "unknown", add=[op])
    s1 = sim(60, "r1", (tools_c, sysb, m0), (tools_f, sysb, m0),
             [eop(TD, saved=TT(cacheRead=1230), note="tools come first, so the 1,230 tokens are before B*")],
             "hot", prev=(tokens(tools_c, sysb, m0[:1]), tokens(tools_f, sysb, m0[:1])))
    s2 = sim(120, "r2", (tools_c, sysb, m1), (tools_f, sysb, m1f),
             [eop(LD, caused=TT(cacheWrite5m=750)), eop(TD, saved=TT(cacheRead=1230))],  # reported this request first, then carried
             "hot", add=[load], prev=(tokens(tools_c, sysb, m0), tokens(tools_f, sysb, m0)))
    write_issue("rewrite/tool-defer-stubs/loaded-later/5m", 60,
                "tool-defer defers 2 tools, as in the first case. On the third request the model loads one of them, "
                "and its full definition (750 tokens) enters the new message. The client body does not hold it, so "
                "the host reports a rewrite that makes message 2 larger, with its own ChangeID. The new message is "
                "after the cached prefix, so the 750 tokens are written, not read: a cost, R33. The deferral "
                "keeps its 1,230 tokens of saving, because the stubs stay in the tools.",
                DIMS("rewrite", "tools", "unknown-hit-miss", spans="true"), [s0, s1, s2])


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    for old in os.listdir(OUT):  # the generator owns this directory: drop cases that no longer exist
        if old.endswith(".json"):
            os.remove(os.path.join(OUT, old))
    for f in (ex12, ex3, ex4, ex5, ex67, ex8, shared_prefix_cases, ex9, c01, c02, c03_04, c05, c06, c07, c08, c09, c10, c11, c12, c_threads, c_client_compaction, c_client_trim_keeps_head, c_client_rewrite_in_place_ends_replay, c_client_compaction_before_any_change, c_client_tail_edit_keeps_replay, c_client_tail_compaction_ends_replay, c_client_two_compactions, c_clock, c_ttl, c_mix,
              c14, s909, r909_system_hash, c15, c16, c17, c18, a1, a2, a3, a4_b3, b1_b2, b4, b5, c1_, c2_, c2b, c3_, c5, d2, d3, d4,
              d5, d6, e2, e345, f245, h2, h5, r9, i53, i54, i56, i55_split, i55_no_cache, i55_media_one_side, r14, r21, r20, r20b, c42, c42b, c42c, c42d, c43, i44, c49, i52_all, d60, d60_1h, d60_loaded):
        f()
