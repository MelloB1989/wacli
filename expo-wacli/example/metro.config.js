const path = require('path');
const { getDefaultConfig } = require('expo/metro-config');

const projectRoot = __dirname;
// The module is this example's workspace root, so its node_modules is where bun hoists everything.
const workspaceRoot = path.resolve(projectRoot, '..');

const config = getDefaultConfig(projectRoot);

// The module's source lives outside the project root. Metro only watches the project root by
// default, so without this an edit to ../src would not rebuild here — and the hoisted packages
// under ../node_modules would not be watched either.
config.watchFolders = [workspaceRoot];

// Resolve from the app first, then the workspace root. bun installs into a content-addressed store
// under <root>/node_modules/.bun and symlinks names into place, so both directories are needed and
// Metro has to follow the symlinks — which it does by default on this React Native version.
config.resolver.nodeModulesPaths = [
  path.resolve(projectRoot, 'node_modules'),
  path.resolve(workspaceRoot, 'node_modules'),
];

// Deliberately no blockList hiding the workspace root's react/react-native. Under a hoisted
// workspace those are not a second copy to defend against — they are the only copy, shared by the
// module and the app, which is what keeps a single React instance and avoids "invalid hook call".
module.exports = config;
