#!/usr/bin/env python3
"""Measure the input bill of sent-back thinking blocks (rossoctl/accountant#77).

For each (model, budget):
  1. Make two assistant turns with thinking. Record each block's thinking_tokens (T) and
     signature bytes (S).
  2. Send a final turn four ways: both thinking blocks, only block 1, only block 2, none.
     The text of every turn stays the same. No cache_control, so input_tokens is the whole bill.
  3. Billed cost of a block = input(with it) - input(without it).

Stdlib only. Reads ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN. Writes the raw requests and responses to a new temporary folder. Results: design D16.
Usage: python3 -I scripts/measure-thinking-bill.py [model:budget ...]
"""
import json, os, sys, tempfile, time, urllib.request, urllib.error

BASE = os.environ["ANTHROPIC_BASE_URL"].rstrip("/")
KEY = os.environ["ANTHROPIC_AUTH_TOKEN"]
OUT = tempfile.mkdtemp(prefix="thinking-bill-")
print("raw output:", OUT, file=sys.stderr)

Q1 = "How many prime numbers are there between 100 and 200? Check each candidate. Answer with the number only."
Q2 = "What is 7^13 mod 1009? Work it out carefully. Answer with the number only."
Q3 = "Reply with the single word OK."


def post(path, body, tag):
    req = urllib.request.Request(
        BASE + path, data=json.dumps(body).encode(), method="POST",
        headers={"x-api-key": KEY, "authorization": "Bearer " + KEY,
                 "anthropic-version": "2023-06-01", "content-type": "application/json"})
    for attempt in range(4):
        try:
            with urllib.request.urlopen(req, timeout=600) as r:
                d = json.load(r)
            break
        except urllib.error.HTTPError as e:
            msg = e.read().decode()[:300]
            if e.code in (429, 500, 502, 503, 529) and attempt < 3:
                time.sleep(5 * (attempt + 1)); continue
            raise SystemExit(f"{tag}: HTTP {e.code}: {msg}")
    with open(os.path.join(OUT, tag + ".json"), "w") as f:
        json.dump({"request": body, "response": d}, f, indent=1)
    return d


def billed_input(u):
    return u["input_tokens"] + u.get("cache_read_input_tokens", 0) + u.get("cache_creation_input_tokens", 0)


def think_info(content):
    blocks = [c for c in content if c["type"] in ("thinking", "redacted_thinking")]
    sig = sum(len(c.get("signature", "")) + len(c.get("data", "")) for c in blocks)
    return len(blocks), sig


def strip_thinking(content):
    return [c for c in content if c["type"] not in ("thinking", "redacted_thinking")]


def run(model, budget):
    tag = f"{model.replace('/', '_')}_b{budget}"
    base = {"model": model, "max_tokens": budget + 400, "thinking": {"type": "enabled", "budget_tokens": budget}}

    u1 = {"role": "user", "content": Q1}
    r1 = post("/v1/messages", {**base, "messages": [u1]}, tag + "_gen1")
    a1 = {"role": "assistant", "content": r1["content"]}
    u2 = {"role": "user", "content": Q2}
    r2 = post("/v1/messages", {**base, "messages": [u1, a1, u2]}, tag + "_gen2")
    a2 = {"role": "assistant", "content": r2["content"]}
    u3 = {"role": "user", "content": Q3}

    t1 = r1["usage"].get("output_tokens_details", {}).get("thinking_tokens")
    t2 = r2["usage"].get("output_tokens_details", {}).get("thinking_tokens")
    n1, s1 = think_info(r1["content"])
    n2, s2 = think_info(r2["content"])

    a1s = {"role": "assistant", "content": strip_thinking(r1["content"])}
    a2s = {"role": "assistant", "content": strip_thinking(r2["content"])}
    variants = {"both": [u1, a1, u2, a2, u3], "only1": [u1, a1, u2, a2s, u3],
                "only2": [u1, a1s, u2, a2, u3], "none": [u1, a1s, u2, a2s, u3]}
    bill = {}
    for name, msgs in variants.items():
        r = post("/v1/messages", {**base, "max_tokens": budget + 50, "messages": msgs}, f"{tag}_{name}")
        bill[name] = billed_input(r["usage"])
    # Repeat "none" once to see if the bill is deterministic.
    r = post("/v1/messages", {**base, "max_tokens": budget + 50, "messages": variants["none"]}, f"{tag}_none_rep")
    bill["none_rep"] = billed_input(r["usage"])

    row = {"model": model, "budget": budget,
           "blk1": {"n": n1, "sig_bytes": s1, "thinking_tokens": t1,
                    "billed_alone": bill["only1"] - bill["none"], "billed_last": bill["both"] - bill["only2"]},
           "blk2": {"n": n2, "sig_bytes": s2, "thinking_tokens": t2,
                    "billed_alone": bill["only2"] - bill["none"], "billed_last": bill["both"] - bill["only1"]},
           "bill": bill}
    print(json.dumps(row), flush=True)
    return row


if __name__ == "__main__":
    plan = [(m, int(b)) for m, b in (a.split(":") for a in sys.argv[1:])] or [
        ("aws/claude-opus-5-5", 1024), ("aws/claude-opus-5-5", 4000), ("aws/claude-opus-5-5", 12000),
        ("aws/claude-sonnet-4-5", 1024), ("aws/claude-sonnet-4-5", 4000)]
    rows = [run(m, b) for m, b in plan]
    with open(os.path.join(OUT, "summary.json"), "w") as f:
        json.dump(rows, f, indent=1)
