import { useCallback, useEffect, useState } from 'react';
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from 'react-native';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';
import { StatusBar } from 'expo-status-bar';
import * as Wacli from 'expo-wacli';
import type { ChatRecord } from 'expo-wacli';

import { ChatScreen } from './src/screens/ChatScreen';
import { ChatsScreen } from './src/screens/ChatsScreen';
import { LoginScreen } from './src/screens/LoginScreen';
import { errorMessage } from './src/format';
import { space, theme } from './src/theme';

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
  | { name: 'chats' }
  | { name: 'chat'; chat: ChatRecord };

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

        {screen.name === 'chats' && (
          <ChatsScreen onOpenChat={(chat) => setScreen({ name: 'chat', chat })} />
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
});
