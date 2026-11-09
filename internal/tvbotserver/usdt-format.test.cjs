const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "ui.go"), "utf8");

function extractFunction(name) {
  const marker = `    function ${name}`;
  const start = source.indexOf(marker);
  assert.notEqual(start, -1, `missing ${name}`);
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") depth -= 1;
    if (depth === 0) return source.slice(start, index + 1);
  }
  throw new Error(`unterminated ${name}`);
}

const context = {};
vm.runInNewContext(
  ["formatFixed2", "formatUSDTAmount", "formatUSDTLabel"].map(extractFunction).join("\n") +
    "\nresult = { formatUSDTAmount, formatUSDTLabel };",
  context,
);

const { formatUSDTAmount, formatUSDTLabel } = context.result;
assert.equal(formatUSDTAmount("310.69892"), "310.70");
assert.equal(formatUSDTAmount("-8.356092"), "-8.36");
assert.equal(formatUSDTAmount(0), "0.00");
assert.equal(formatUSDTAmount(""), "-");
assert.equal(formatUSDTAmount("not-a-number"), "-");
assert.equal(formatUSDTLabel("1.2"), "1.20 USDT");
assert.equal(formatUSDTLabel(""), "-");
