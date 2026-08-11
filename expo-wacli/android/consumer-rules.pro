# gomobile reaches the Go runtime through JNI, resolving these classes and the callback interfaces
# by name at runtime. R8 has no way to see those references and would strip or rename them.
-keep class go.** { *; }
-keep class mobile.** { *; }
-keep class com.wacli.mobile.** { *; }
-keep class com.wacli.expo.** { *; }
