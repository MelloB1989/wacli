require 'json'

package = JSON.parse(File.read(File.join(__dir__, '..', 'package.json')))

Pod::Spec.new do |s|
  s.name           = 'ExpoWacli'
  s.version        = package['version']
  s.summary        = package['description']
  s.description    = package['description']
  s.license        = package['license']
  s.author         = package['author']
  s.homepage       = package['homepage']
  # Tracks expo-modules-core's own floor for SDK 56. Raising it here would break apps that build
  # fine against the rest of the SDK; lowering it would not link.
  s.platforms      = {
    :ios => '16.4'
  }
  s.swift_version  = '5.9'
  s.source         = { git: 'https://github.com/MelloB1989/wacli.git' }
  s.static_framework = true

  s.dependency 'ExpoModulesCore'

  # Produced by scripts/build-bindings.sh. Not in git: it is a ~60 MB build artifact carrying a
  # slice per architecture, not source.
  s.vendored_frameworks = 'Frameworks/Mobile.xcframework'

  # Scoped so the vendored framework's own headers are not swept into the build.
  s.source_files = '*.swift'

  s.pod_target_xcconfig = {
    'DEFINES_MODULE' => 'YES',
    'SWIFT_COMPILATION_MODE' => 'wholemodule'
  }
end
