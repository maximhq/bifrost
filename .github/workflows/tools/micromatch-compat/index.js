'use strict';

const picomatch = require('picomatch');

// Bound recursion before handing user-supplied patterns to the glob parser.
function checkPattern(pattern) {
  if (typeof pattern !== 'string') throw new TypeError('Glob pattern must be a string');
  let depth = 0;
  for (let index = 0; index < pattern.length; index++) {
    const char = pattern[index];
    if (char === '\\') { index++; continue; }
    if (char === '{' || char === '(') {
      if (++depth > 64) throw new RangeError('Glob pattern exceeds nesting limit of 64');
    } else if (char === '}' || char === ')') {
      depth = Math.max(0, depth - 1);
    }
  }
  return pattern;
}

function matcher(patterns, options) {
  for (const pattern of [].concat(patterns)) checkPattern(pattern);
  return picomatch(patterns, options);
}

function match(files, patterns, options = {}) {
  const inputs = [].concat(files);
  const globs = [].concat(patterns).map(checkPattern);
  const included = new Set();
  const excluded = new Set();
  const outputs = new Set();
  let allNegative = globs.length > 0;
  for (const glob of globs) {
    const test = picomatch(glob, options, true);
    const negative = test.state.negated || test.state.negatedExtglob;
    allNegative = allNegative && negative;
    for (const file of inputs) {
      const result = test(file, true);
      outputs.add(result.output);
      if (negative ? !result.isMatch : result.isMatch) {
        if (negative) excluded.add(result.output);
        else {
          excluded.delete(result.output);
          included.add(result.output);
        }
      }
    }
  }
  const matches = [...(allNegative ? outputs : included)].filter((file) => !excluded.has(file));
  if (!matches.length) {
    if (options.failglob) throw new Error(`No matches found for "${globs.join(', ')}"`);
    if (options.nonull || options.nullglob) {
      return options.unescape ? globs.map((glob) => glob.replace(/\\/g, '')) : globs;
    }
  }
  return matches;
}

match.match = match;
match.matcher = matcher;
match.isMatch = (file, patterns, options) => matcher(patterns, options)(file);
module.exports = match;
