#!/usr/bin/env bash
# Probe the live Anthropic Messages API for every tool-surface behaviour the
# OpenAI-to-Anthropic translation has to get right.
#
# This exists because the translation must be built from what the API actually
# does, not from a remembered schema. Each probe prints the shape the API
# returned, so the mapping table in the PR cites observed behaviour.
#
# Usage:  ANTHROPIC_API_KEY=... ./scripts/dev/probe-anthropic-tools.sh
# Writes raw responses to a temp dir and prints a summary.
set -uo pipefail

: "${ANTHROPIC_API_KEY:?set ANTHROPIC_API_KEY}"
MODEL="${PROBE_MODEL:-claude-haiku-4-5}"
OUT="${PROBE_OUT:-$(mktemp -d)}"
VER="2023-06-01"

echo "probing $MODEL; raw responses in $OUT"
echo

call() { # call <name> <json-body>
  local name=$1 body=$2
  local code
  code=$(curl -sS https://api.anthropic.com/v1/messages \
    -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: $VER" \
    -H "content-type: application/json" -d "$body" \
    -o "$OUT/$name.json" -w '%{http_code}')
  printf '%s' "$code"
}

show() { python3 - "$OUT/$1.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
if 'error' in d:
    print('   error:', d['error'].get('type'), '-', d['error'].get('message','')[:160]); raise SystemExit
print('   stop_reason:', d.get('stop_reason'))
for b in d.get('content',[]):
    if b['type']=='tool_use':
        print(f"   content block: type=tool_use id={b.get('id')} name={b.get('name')} input={json.dumps(b.get('input'))}")
    elif b['type']=='text':
        print(f"   content block: type=text text={b.get('text','')[:60]!r}")
    else:
        print(f"   content block: type={b['type']}")
PY
}

TOOL='{"name":"get_weather","description":"Get the weather for a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}'

echo "1. tool definition shape: does it take input_schema (not parameters)?"
echo "   HTTP $(call t1 "{\"model\":\"$MODEL\",\"max_tokens\":256,\"tools\":[$TOOL],\"messages\":[{\"role\":\"user\",\"content\":\"weather in Paris?\"}]}")"
show t1

echo
echo "1b. control: does 'parameters' (the OpenAI spelling) get rejected?"
BAD='{"name":"get_weather","description":"d","parameters":{"type":"object"}}'
echo "   HTTP $(call t1b "{\"model\":\"$MODEL\",\"max_tokens\":64,\"tools\":[$BAD],\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")"
show t1b

echo
echo "2. tool_choice vocabulary"
for tc in '{"type":"auto"}' '{"type":"any"}' '{"type":"tool","name":"get_weather"}' '{"type":"none"}' '{"type":"required"}' '"auto"'; do
  n=$(echo "$tc" | tr -cd 'a-z')
  echo "   tool_choice=$tc -> HTTP $(call tc_$n "{\"model\":\"$MODEL\",\"max_tokens\":128,\"tools\":[$TOOL],\"tool_choice\":$tc,\"messages\":[{\"role\":\"user\",\"content\":\"weather in Paris?\"}]}")"
  show tc_$n | head -3
done

echo
echo "3. tool_result round trip, and is_error signalling"
RT='{"model":"'$MODEL'","max_tokens":256,"tools":['$TOOL'],"messages":[
 {"role":"user","content":"weather in Paris?"},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_probe1","name":"get_weather","input":{"city":"Paris"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_probe1","content":"18C and clear"}]}]}'
echo "   HTTP $(call t3 "$RT")"; show t3

ERR='{"model":"'$MODEL'","max_tokens":256,"tools":['$TOOL'],"messages":[
 {"role":"user","content":"weather in Paris?"},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_probe2","name":"get_weather","input":{"city":"Paris"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_probe2","content":"service down","is_error":true}]}]}'
echo "   is_error=true -> HTTP $(call t3e "$ERR")"; show t3e

echo
echo "3b. does a tool_result on an assistant turn work? (OpenAI puts it on role=tool)"
WRONG='{"model":"'$MODEL'","max_tokens":128,"tools":['$TOOL'],"messages":[
 {"role":"user","content":"weather?"},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_p3","name":"get_weather","input":{"city":"Paris"}}]},
 {"role":"assistant","content":[{"type":"tool_result","tool_use_id":"toolu_p3","content":"18C"}]}]}'
echo "   HTTP $(call t3b "$WRONG")"; show t3b

echo
echo "4. parallel tool calls, and disable_parallel_tool_use"
T2='{"name":"get_time","description":"Get the current time in a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}'
PAR='{"model":"'$MODEL'","max_tokens":512,"tools":['$TOOL','$T2'],"messages":[{"role":"user","content":"What is the weather AND the time in Paris? Call both tools."}]}'
echo "   HTTP $(call t4 "$PAR")"; show t4
DIS='{"model":"'$MODEL'","max_tokens":512,"tools":['$TOOL','$T2'],"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"What is the weather AND the time in Paris? Call both tools."}]}'
echo "   disable_parallel_tool_use -> HTTP $(call t4d "$DIS")"; show t4d

echo
echo "5. stop_reason vocabulary observed above:"
for f in "$OUT"/*.json; do
  python3 -c "
import json,sys
try: d=json.load(open('$f'))
except Exception: raise SystemExit
if d.get('stop_reason'): print('  ', d['stop_reason'])" 
done | sort -u

echo
echo "6. streaming: input_json_delta fragments and index assignment"
curl -sS -N https://api.anthropic.com/v1/messages \
  -H "x-api-key: $ANTHROPIC_API_KEY" -H "anthropic-version: $VER" -H "content-type: application/json" \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":512,\"stream\":true,\"tools\":[$TOOL,$T2],\"messages\":[{\"role\":\"user\",\"content\":\"What is the weather AND the time in Paris? Call both tools.\"}]}" \
  > "$OUT/stream.txt" 2>&1
echo "   event sequence:"
grep -oE '"type":"[a-z_]+"' "$OUT/stream.txt" | sed 's/"type":"//;s/"//' | uniq -c | sed 's/^/     /'
echo "   content_block_start blocks:"
# The SSE payload is the whole `data:` line. Matching from the "type" key drops
# the opening brace, so every json.loads below failed and this section printed
# nothing at all, which is indistinguishable from a stream that carried no tool
# blocks. Strip the `data: ` prefix and parse the object instead, and say so
# when a line will not parse rather than swallowing it.
grep '"type":"content_block_start"' "$OUT/stream.txt" | sed 's/^data: *//' | python3 -c "
import sys,json
seen=0
for l in sys.stdin:
    l=l.strip()
    if not l: continue
    try: d=json.loads(l)
    except Exception as e:
        print(f\"     unparsed: {e}: {l[:120]}\")
        continue
    seen+=1
    cb=d.get('content_block',{})
    print(f\"     index={d.get('index')} type={cb.get('type')} id={cb.get('id')} name={cb.get('name')} input={json.dumps(cb.get('input'))}\")
if not seen: print('     (no content_block_start events found)')"
echo "   input_json_delta fragments (first 8):"
grep '"type":"input_json_delta"' "$OUT/stream.txt" | sed 's/^data: *//' | head -8 | sed 's/^/     /'
echo "   indices seen on content_block_delta:"
grep -o '"type":"content_block_delta"[^}]*' "$OUT/stream.txt" | grep -oE '"index":[0-9]+' | sort -u | sed 's/^/     /'

echo
echo "raw responses: $OUT"
