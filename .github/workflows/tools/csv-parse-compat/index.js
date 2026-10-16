const { parse } = require('csv-parse7');

// Newman calls the v4 default export with (data, options, callback). Since v5,
// parse is a named export and the old `relax` option is called `relax_quotes`.
// Keep the parsing itself in the patched upstream package.
module.exports = function (data, options, callback) {
  const { relax, ...settings } = options;
  return parse(data, {
    ...settings,
    relax_quotes: settings.relax_quotes ?? relax,
  }, callback);
};
