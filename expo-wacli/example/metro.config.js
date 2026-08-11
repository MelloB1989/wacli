const path = require('path');
const { getDefaultConfig } = require('expo/metro-config');

const projectRoot = __dirname;
const moduleRoot = path.resolve(projectRoot, '..');

const config = getDefaultConfig(projectRoot);

// expo-wacli is linked with `file:..`, which puts its source outside the project root. Metro only
// watches the project root by default, so without this an edit to ../src would not rebuild here.
config.watchFolders = [moduleRoot];

// Resolve from the app first and the module second, never the reverse.
config.resolver.nodeModulesPaths = [
  path.resolve(projectRoot, 'node_modules'),
  path.resolve(moduleRoot, 'node_modules'),
];

// The module keeps expo and react as dev dependencies for its own typechecking. Two copies of React
// in one bundle is an "invalid hook call" crash that costs an afternoon to diagnose, so the
// module's copies are hidden from the bundler entirely.
config.resolver.blockList = [
  new RegExp(`^${escapeRegExp(path.join(moduleRoot, 'node_modules', 'react'))}\\b.*$`),
  new RegExp(`^${escapeRegExp(path.join(moduleRoot, 'node_modules', 'react-native'))}\\b.*$`),
  new RegExp(`^${escapeRegExp(path.join(moduleRoot, 'node_modules', 'expo'))}\\b.*$`),
  new RegExp(`^${escapeRegExp(path.join(moduleRoot, 'example'))}.*$`),
];

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

module.exports = config;
