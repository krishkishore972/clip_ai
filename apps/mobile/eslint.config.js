const { defineConfig } = require('eslint/config');
const expoConfig = require('eslint-config-expo/flat');
const { fixupConfigRules } = require('@eslint/compat');

module.exports = defineConfig([
  // Expo's React plugin still uses removed RuleContext methods on ESLint 10.
  // Restore compatibility without disabling its rules or pinning EOL ESLint 9.
  ...fixupConfigRules(expoConfig),
  { ignores: ['dist/**', '.expo/**'] },
  { files: ['scripts/**/*.mjs', 'tests/**/*.cjs'], languageOptions: { globals: { process: 'readonly', Buffer: 'readonly' } } },
]);
