Pod::Spec.new do |s|
  s.name           = 'ExpoWacli'
  s.version        = '0.1.0'
  s.summary        = 'Run wacli inside a React Native Expo app.'
  s.description    = 'Expo module wrapping the wacli WhatsApp automation daemon, bound for iOS with gomobile.'
  s.author         = 'MelloB1989'
  s.homepage       = 'https://github.com/MelloB1989/wacli'
  s.license        = { :type => 'MIT' }
  s.platforms      = { :ios => '15.1', :tvos => '15.1' }
  s.swift_version  = '5.9'
  s.source         = { :git => 'https://github.com/MelloB1989/wacli.git' }
  s.static_framework = true

  s.dependency 'ExpoModulesCore'

  # Produced by scripts/build-bindings.sh. Not in git: it is a ~60 MB build artifact carrying a
  # slice per architecture, not source.
  s.vendored_frameworks = 'Frameworks/Mobile.xcframework'

  # Scoped to this directory so the vendored framework's own headers are not swept into the build.
  s.source_files = '*.swift'

  s.pod_target_xcconfig = {
    'DEFINES_MODULE' => 'YES',
    'SWIFT_COMPILATION_MODE' => 'wholemodule'
  }
end
