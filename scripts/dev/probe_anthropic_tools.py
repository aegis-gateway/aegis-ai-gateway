#!/usr/bin/env python3
"""Probe the live Anthropic Messages API for the tool-surface behaviour the
OpenAI-to-Anthropic translation depends on.

The translation must be built from what the API does, not from a remembered
schema, so every mapping claim in the PR cites a line of this output.

Request bodies are built with json.dumps rather than interpolated into shell
strings. The first version of this probe did the latter and silently sent
malformed JSON for three of its six sections, which produced a wall of HTTP 400
that looked like an API rejection rather than a bug in the probe.

Usage: ANTHROPIC_API_KEY=... python3 scripts/dev/probe_anthropic_tools.py
"""
import json, os, sys, urllib.request, urllib.error

KEY = os.environ.get("ANTHROPIC_API_KEY")
if not KEY:
    sys.exit("set ANTHROPIC_API_KEY")
MODEL = os.environ.get("PROBE_MODEL", "claude-haiku-4-5")
URL = "https://api.anthropic.com/v1/messages"

WEATHER = {"name": "get_weather", "description": "Get the weather for a city",
           "input_schema": {"type": "object", "properties": {"city": {"type": "string"}},
                            "required": ["city"]}}
TIME = {"name": "get_time", "description": "Get the current time in a city",
        "input_schema": {"type": "object", "properties": {"city": {"type": "string"}},
                         "required": ["city"]}}


def call(body, stream=False):
    """Returns (status, parsed-or-raw). Never raises on an HTTP error status."""
    data = json.dumps(body).encode()
    req = urllib.request.Request(URL, data=data, headers={
        "x-api-key": KEY, "anthropic-version": "2023-06-01",
        "content-type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read().decode()
            return r.status, (raw if stream else json.loads(raw))
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw


def describe(status, d):
    if isinstance(d, dict) and "error" in d:
        return f"HTTP {status}  {d['error'].get('type')}: {d['error'].get('message','')[:150]}"
    out = [f"HTTP {status}  stop_reason={d.get('stop_reason')}"]
    for b in d.get("content", []):
        if b["type"] == "tool_use":
            out.append(f"      tool_use id={b['id']} name={b['name']} input={json.dumps(b['input'])}")
        elif b["type"] == "text":
            out.append(f"      text {b.get('text','')[:70]!r}")
        else:
            out.append(f"      {b['type']}")
    return "\n".join(out)


def base(**kw):
    b = {"model": MODEL, "max_tokens": 512,
         "messages": [{"role": "user", "content": "What is the weather in Paris?"}]}
    b.update(kw)
    return b


print(f"probing {MODEL}\n")

print("1. TOOL DEFINITION SHAPE")
print("   input_schema:", describe(*call(base(tools=[WEATHER]))))
bad = {"name": "get_weather", "description": "d", "parameters": {"type": "object"}}
print("   parameters (the OpenAI spelling):", describe(*call(base(tools=[bad]))))

print("\n2. TOOL_CHOICE VOCABULARY")
for tc in [{"type": "auto"}, {"type": "any"}, {"type": "tool", "name": "get_weather"},
           {"type": "none"}, {"type": "required"}, "auto", {"type": "function", "function": {"name": "get_weather"}}]:
    s, d = call(base(tools=[WEATHER], tool_choice=tc))
    first = describe(s, d).split("\n")[0]
    print(f"   {json.dumps(tc):<62} {first}")

print("\n3. TOOL RESULT POSITIONING AND ERRORS")
hist = [{"role": "user", "content": "weather in Paris?"},
        {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_p1",
                                           "name": "get_weather", "input": {"city": "Paris"}}]}]
ok = hist + [{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_p1",
                                           "content": "18C and clear"}]}]
print("   result on a user turn:  ", describe(*call(base(tools=[WEATHER], messages=ok))))
err = hist + [{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_p1",
                                            "content": "service down", "is_error": True}]}]
print("   is_error=true:          ", describe(*call(base(tools=[WEATHER], messages=err))))
wrong = hist + [{"role": "assistant", "content": [{"type": "tool_result", "tool_use_id": "toolu_p1",
                                                   "content": "18C"}]}]
print("   result on an assistant turn:", describe(*call(base(tools=[WEATHER], messages=wrong))))
blocks = hist + [{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_p1",
                                               "content": [{"type": "text", "text": "18C and clear"}]}]}]
print("   result content as blocks:", describe(*call(base(tools=[WEATHER], messages=blocks))).split("\n")[0])
missing = hist + [{"role": "user", "content": [{"type": "tool_result", "content": "18C"}]}]
print("   result with no tool_use_id:", describe(*call(base(tools=[WEATHER], messages=missing))).split("\n")[0])
orphan = [{"role": "user", "content": "hi"},
          {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_nope", "content": "x"}]}]
print("   result with unknown id: ", describe(*call(base(tools=[WEATHER], messages=orphan))).split("\n")[0])

print("\n4. PARALLEL TOOL CALLS")
both = base(tools=[WEATHER, TIME],
            messages=[{"role": "user", "content": "What is the weather AND the time in Paris? Call both tools."}])
print("   default:               ", describe(*call(both)))
both_off = dict(both, tool_choice={"type": "auto", "disable_parallel_tool_use": True})
print("   disable_parallel:      ", describe(*call(both_off)))

print("\n5. STOP_REASON when a tool is called:")
s, d = call(base(tools=[WEATHER]))
print(f"   tool call -> {d.get('stop_reason')}")
s, d = call(base(tools=[WEATHER], tool_choice={"type": "none"}))
print(f"   tool_choice none -> {d.get('stop_reason')}")
s, d = call(base(max_tokens=1))
print(f"   truncated -> {d.get('stop_reason')}")

print("\n5b. STRICT TOOLS AND additionalProperties")
strict_cases = [
    ("strict, root sets additionalProperties:false",
     {"type": "object", "additionalProperties": False, "properties": {"x": {"type": "string"}}}),
    ("strict, root omits it",
     {"type": "object", "properties": {"x": {"type": "string"}}}),
    ("strict, nested object omits it",
     {"type": "object", "additionalProperties": False,
      "properties": {"n": {"type": "object", "properties": {"x": {"type": "string"}}}}}),
    ("strict, nested object sets it",
     {"type": "object", "additionalProperties": False,
      "properties": {"n": {"type": "object", "additionalProperties": False,
                           "properties": {"x": {"type": "string"}}}}}),
    ("strict, no properties, additionalProperties:false (AEGIS's own default)",
     {"type": "object", "properties": {}, "additionalProperties": False}),
    ("strict, no properties, no additionalProperties",
     {"type": "object", "properties": {}}),
    ("strict, additionalProperties:false, no `required` key",
     {"type": "object", "additionalProperties": False, "properties": {"x": {"type": "string"}}}),
    ("strict, object inside array items omits it",
     {"type": "object", "additionalProperties": False,
      "properties": {"l": {"type": "array", "items": {"type": "object",
                                                      "properties": {"y": {"type": "string"}}}}}}),
]
for label, schema in strict_cases:
    s_, d_ = call(base(tools=[{"name": "f", "strict": True, "input_schema": schema}],
                       messages=[{"role": "user", "content": "hi"}]))
    detail = d_["error"]["message"][:90] if isinstance(d_, dict) and "error" in d_ else "accepted"
    print(f"   {label:<48} HTTP {s_}  {detail}")

print("\n6. STREAMING: tool call deltas and index assignment")
s, raw = call(dict(both, stream=True), stream=True)
events, starts, deltas, idx = [], [], [], set()
for line in raw.splitlines():
    if not line.startswith("data: "):
        continue
    try:
        ev = json.loads(line[6:])
    except ValueError:
        print("   UNPARSED:", line[:70]); continue
    events.append(ev.get("type"))
    if ev.get("type") == "content_block_start":
        cb = ev.get("content_block", {})
        starts.append((ev.get("index"), cb.get("type"), cb.get("id"), cb.get("name"), json.dumps(cb.get("input"))))
    if ev.get("type") == "content_block_delta":
        idx.add(ev.get("index"))
        dl = ev.get("delta", {})
        if dl.get("type") == "input_json_delta":
            deltas.append((ev.get("index"), dl.get("partial_json")))
print("   event types in order:", " ".join(dict.fromkeys(events)))
print("   content_block_start:")
for st in starts:
    print(f"      index={st[0]} type={st[1]} id={st[2]} name={st[3]} input={st[4]}")
print(f"   input_json_delta fragments: {len(deltas)} across indices {sorted(idx)}")
for d_ in deltas[:10]:
    print(f"      index={d_[0]} partial_json={d_[1]!r}")
acc = {}
for i, frag in deltas:
    acc[i] = acc.get(i, "") + (frag or "")
print("   reassembled by index:")
for i in sorted(acc):
    try:
        parsed = json.loads(acc[i]); ok_ = "valid JSON"
    except ValueError:
        parsed = acc[i]; ok_ = "INVALID"
    print(f"      index={i} -> {acc[i]!r}  ({ok_})")
