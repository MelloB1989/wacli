import { useCallback, useEffect, useState } from 'react';
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';
import { StatusBar } from 'expo-status-bar';
import * as Wacli from 'expo-wacli';
import type { ChatRecord } from 'expo-wacli';

import { ChatScreen } from './src/screens/ChatScreen';
import { ChatsScreen } from './src/screens/ChatsScreen';
import { ConsoleScreen } from './src/screens/ConsoleScreen';
import { LoginScreen } from './src/screens/LoginScreen';
import { errorMessage } from './src/format';
import { radius, space, theme } from './src/theme';

/**
 * expo-wacli demo.
 *
 * Deliberately dependency-light — no navigation library, just a screen union — so the interesting
 * part of the file is the wacli lifecycle rather than the plumbing around it.
 */
type Screen =
  | { name: 'booting' }
  | { name: 'failed'; message: string }
  | { name: 'login' }
  // One member rather than two, so a tab can be selected by name without widening complaints.
  | { name: 'chats' | 'console' }
  | { name: 'chat'; chat: ChatRecord };

/** The tabs are the two things worth having side by side: the app view and the raw one. */
const TABS = [
  { name: 'chats', label: 'Chats' },
  { name: 'console', label: 'Console' },
] as const;

export default function App() {
  const [screen, setScreen] = useState<Screen>({ name: 'booting' });

  const boot = useCallback(async () => {
    setScreen({ name: 'booting' });
    try {
      // The whole decision: is there a session to resume, or does the user need to link one?
      if (await Wacli.isPaired()) {
        await Wacli.start();
        setScreen({ name: 'chats' });
      } else {
        setScreen({ name: 'login' });
      }
    } catch (e) {
      setScreen({ name: 'failed', message: errorMessage(e) });
    }
  }, []);

  useEffect(() => {
    boot();
  }, [boot]);

  return (
    <SafeAreaProvider>
      <StatusBar style="light" />
      <SafeAreaView style={styles.root} edges={['top', 'bottom']}>
        {screen.name === 'booting' && (
          <View style={styles.centered}>
            <ActivityIndicator color={theme.accent} />
            <Text style={styles.bootText}>Starting wacli…</Text>
          </View>
        )}

        {screen.name === 'failed' && (
          <View style={styles.centered}>
            <Text style={styles.failedTitle}>Could not start</Text>
            <Text style={styles.failedText}>{screen.message}</Text>
            <Pressable style={styles.retry} onPress={boot}>
              <Text style={styles.retryText}>Try again</Text>
            </Pressable>
          </View>
        )}

        {screen.name === 'login' && (
          <LoginScreen
            onPaired={() => {
              // StartLogin leaves the service running on success, so there is nothing to start —
              // go straight to the chat list.
              setScreen({ name: 'chats' });
            }}
          />
        )}

        {/* Both tabs stay mounted so switching away does not throw away console scrollback or the
            chat list's scroll position. */}
        {(screen.name === 'chats' || screen.name === 'console') && (
          <>
            <View style={[styles.tabBody, screen.name !== 'chats' && styles.hidden]}>
              <ChatsScreen onOpenChat={(chat) => setScreen({ name: 'chat', chat })} />
            </View>
            <View style={[styles.tabBody, screen.name !== 'console' && styles.hidden]}>
              <ConsoleScreen />
            </View>
            <View style={styles.tabBar}>
              {TABS.map((tab) => (
                <Pressable
                  key={tab.name}
                  style={[styles.tab, screen.name === tab.name && styles.tabActive]}
                  onPress={() => setScreen({ name: tab.name })}
                >
                  <Text
                    style={[styles.tabText, screen.name === tab.name && styles.tabTextActive]}
                  >
                    {tab.label}
                  </Text>
                </Pressable>
              ))}
            </View>
          </>
        )}

        {screen.name === 'chat' && (
          <ChatScreen chat={screen.chat} onBack={() => setScreen({ name: 'chats' })} />
        )}
      </SafeAreaView>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: theme.bg },
  centered: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: space.md, padding: space.xl },
  bootText: { color: theme.textMuted },
  failedTitle: { color: theme.text, fontSize: 20, fontWeight: '700' },
  failedText: { color: theme.textMuted, textAlign: 'center', lineHeight: 20 },
  retry: {
    marginTop: space.md,
    backgroundColor: theme.accent,
    paddingHorizontal: space.xl,
    paddingVertical: space.md,
    borderRadius: 12,
  },
  retryText: { color: theme.accentText, fontWeight: '700' },

  tabBody: { flex: 1 },
  // display:none rather than unmounting — see the note at the call site.
  hidden: { display: 'none' },
  tabBar: {
    flexDirection: 'row',
    gap: space.sm,
    padding: space.sm,
    borderTopWidth: 1,
    borderTopColor: theme.border,
    backgroundColor: theme.surface,
  },
  tab: {
    flex: 1,
    alignItems: 'center',
    paddingVertical: space.sm,
    borderRadius: radius.sm,
  },
  tabActive: { backgroundColor: theme.surfaceAlt },
  tabText: { color: theme.textMuted, fontWeight: '600' },
  tabTextActive: { color: theme.text },
});
