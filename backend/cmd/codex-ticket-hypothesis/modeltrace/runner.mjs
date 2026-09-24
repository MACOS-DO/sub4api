import { webcrypto } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { generateChallenges } from './challenge-browser.mjs';
import { analyzeGlobalOutputs, parseNumbers } from './fingerprint-core.mjs';

globalThis.crypto ??= webcrypto;

try {
  const bank = JSON.parse(await readFile(new URL('./unified_bank.json', import.meta.url), 'utf8'));
  if (!Array.isArray(bank.models) || bank.models.length === 0) {
    throw new Error('ModelTrace fingerprint bank is empty');
  }

  if (process.argv[2] === 'challenges') {
    process.stdout.write(JSON.stringify({ challenges: generateChallenges(1) }));
  } else if (process.argv[2] === 'analyze') {
    let input = '';
    for await (const chunk of process.stdin) input += chunk;
    const { outputs } = JSON.parse(input);
    const diagnostics = outputs.map(({ text, expected_count }, index) => ({
      index,
      parsed_numbers: parseNumbers(text).length,
      minimum_numbers: Math.max(80, Math.ceil(expected_count * 0.55)),
    }));
    try {
      process.stdout.write(JSON.stringify(analyzeGlobalOutputs(outputs, bank)));
    } catch (error) {
      process.stdout.write(JSON.stringify({
        used_outputs: 0,
        diagnostics,
        error: error instanceof Error ? error.message : String(error),
      }));
    }
  } else {
    throw new Error('unknown ModelTrace command');
  }
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
