import assert from "node:assert/strict";
import test from "node:test";

import { LogAssembler, parseDockerLogLine } from "../../internal/web/assets/log-buffer.mjs";

const baseEvent = {
  type: "log",
  container_id: "a".repeat(64),
  container_name: "web",
  stream: "stdout",
  observed_at: "2026-09-15T01:02:03.456Z",
};

test("assembles a log line split across Docker stream chunks", () => {
  const assembler = new LogAssembler();
  assert.deepEqual(assembler.push({ ...baseEvent, message: "2026-09-15T01:02:03.123456789Z hel" }), []);
  const records = assembler.push({ ...baseEvent, message: "lo\n" });
  assert.equal(records.length, 1);
  assert.equal(records[0].message, "hello");
  assert.equal(records[0].sourceTimestamp, "2026-09-15T01:02:03.123456789Z");
});

test("flush preserves the final log line without a newline", () => {
  const assembler = new LogAssembler();
  assembler.push({ ...baseEvent, message: "2026-09-15T01:02:03.999Z final line" });
  const records = assembler.flush();
  assert.equal(records.length, 1);
  assert.equal(records[0].message, "final line");
  assert.equal(assembler.flush().length, 0);
});

test("caps an unterminated line instead of growing pending memory forever", () => {
  const assembler = new LogAssembler(8);
  const records = assembler.push({ ...baseEvent, message: "123456789" });
  assert.equal(records.length, 1);
  assert.match(records[0].message, /^12345678/);
  assert.equal(assembler.flush()[0].message, "9");
});

test("keeps messages without Docker timestamps", () => {
  const parsed = parseDockerLogLine("plain message", baseEvent.observed_at);
  assert.equal(parsed.sourceTimestamp, "");
  assert.equal(parsed.observedAt, baseEvent.observed_at);
  assert.equal(parsed.message, "plain message");
});
