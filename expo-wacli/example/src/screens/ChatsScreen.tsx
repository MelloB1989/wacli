import { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  Pressable,
  RefreshControl,
  StyleSheet,
  Switch,
  Text,
  View,
} from 'react-native';
import * as Wacli from 'expo-wacli';
import type { ChatRecord } from 'expo-wacli';

import { displayName, errorMessage, formatTime } from '../format';
import { useWacliStatus } from '../useWacliStatus';
import { radius, space, theme } from '../theme';

export function ChatsScreen({ onOpenChat }: { onOpenChat: (chat: ChatRecord) => void }) {
  const { status, refresh, setDND } = useWacliStatus();
  const [chats, setChats] = useState<ChatRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setChats(await Wacli.listChats({ limit: 100 }));
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    // Any new message reorders the list. connection_state matters too: on iOS the module stops the
    // service on background and restarts it on return, and whatever landed while the app was away
    // is in the database by then rather than in the event stream.
    const subscription = Wacli.addListener('onEvent', ({ event }) => {
      if (
        event === 'incoming_message' ||
        event === 'outgoing_message' ||
        event === 'connection_state'
      ) {
        load();
      }
    });
    return () => subscription.remove();
  }, [load]);

  // A full sync pulls fresh state from WhatsApp; the local list reload is the cheap part.
  const pullToRefresh = useCallback(async () => {
    setSyncing(true);
    try {
      await Wacli.sync();
      await Promise.all([load(), refresh()]);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSyncing(false);
    }
  }, [load, refresh]);

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={theme.accent} />
      </View>
    );
  }

  return (
    <View style={styles.flex}>
      <View style={styles.header}>
        <View style={styles.flex}>
          <Text style={styles.title}>Chats</Text>
          <View style={styles.statusLine}>
            <View
              style={[
                styles.dot,
                { backgroundColor: status.connected ? theme.accent : theme.warning },
              ]}
            />
            <Text style={styles.subtitle}>
              {status.connected ? 'Connected' : 'Reconnecting…'} · {status.chatCount} chats ·{' '}
              {status.messageCount} messages
            </Text>
          </View>
        </View>
      </View>

      {/*
        DND is wacli's automation gate and it ships off, so a demo that skipped it would just look
        broken the first time you tried to send. Surfacing it is the honest version.
      */}
      <View style={styles.dndBar}>
        <View style={styles.flex}>
          <Text style={styles.dndTitle}>Allow sending</Text>
          <Text style={styles.dndHint}>
            {status.dndMode
              ? 'Outbound messages are armed.'
              : 'Off by default — nothing can be sent until you turn this on.'}
          </Text>
        </View>
        <Switch
          value={status.dndMode}
          onValueChange={(next) => {
            setDND(next).catch((e) => setError(errorMessage(e)));
          }}
          trackColor={{ true: theme.accent, false: theme.border }}
        />
      </View>

      {error && <Text style={styles.error}>{error}</Text>}

      <FlatList
        data={chats}
        keyExtractor={(chat) => chat.jid}
        refreshControl={
          <RefreshControl
            refreshing={syncing}
            onRefresh={pullToRefresh}
            tintColor={theme.textMuted}
          />
        }
        ItemSeparatorComponent={() => <View style={styles.separator} />}
        ListEmptyComponent={
          <View style={styles.empty}>
            <Text style={styles.emptyText}>No chats yet.</Text>
            <Text style={styles.emptyHint}>Pull down to sync from WhatsApp.</Text>
          </View>
        }
        renderItem={({ item }) => (
          <Pressable
            style={({ pressed }) => [styles.row, pressed && styles.rowPressed]}
            onPress={() => onOpenChat(item)}
          >
            <View style={styles.avatar}>
              <Text style={styles.avatarText}>
                {displayName(item.name, item.jid).slice(0, 1).toUpperCase()}
              </Text>
            </View>
            <View style={styles.flex}>
              <View style={styles.rowTop}>
                <Text style={styles.rowName} numberOfLines={1}>
                  {displayName(item.name, item.jid)}
                </Text>
                <Text style={styles.rowTime}>{formatTime(item.last_message_at)}</Text>
              </View>
              <View style={styles.rowTop}>
                <Text style={styles.rowPreview} numberOfLines={1}>
                  {item.last_message_preview || 'No messages'}
                </Text>
                {item.locked && <Text style={styles.lockBadge}>locked</Text>}
              </View>
            </View>
          </Pressable>
        )}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  centered: { flex: 1, alignItems: 'center', justifyContent: 'center' },
  header: {
    paddingHorizontal: space.lg,
    paddingTop: space.md,
    paddingBottom: space.sm,
    flexDirection: 'row',
    alignItems: 'center',
  },
  title: { color: theme.text, fontSize: 26, fontWeight: '700' },
  subtitle: { color: theme.textMuted, fontSize: 12 },
  statusLine: { flexDirection: 'row', alignItems: 'center', gap: space.sm, marginTop: space.xs },
  dot: { width: 8, height: 8, borderRadius: 4 },
  dndBar: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: space.md,
    marginHorizontal: space.lg,
    marginBottom: space.md,
    padding: space.md,
    backgroundColor: theme.surface,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: theme.border,
  },
  dndTitle: { color: theme.text, fontWeight: '600' },
  dndHint: { color: theme.textMuted, fontSize: 12, marginTop: 2 },
  error: {
    color: theme.danger,
    paddingHorizontal: space.lg,
    paddingBottom: space.sm,
  },
  separator: { height: StyleSheet.hairlineWidth, backgroundColor: theme.border, marginLeft: 72 },
  row: { flexDirection: 'row', alignItems: 'center', gap: space.md, padding: space.md },
  rowPressed: { backgroundColor: theme.surface },
  rowTop: { flexDirection: 'row', alignItems: 'center', gap: space.sm },
  rowName: { color: theme.text, fontSize: 16, fontWeight: '600', flex: 1 },
  rowTime: { color: theme.textMuted, fontSize: 12 },
  rowPreview: { color: theme.textMuted, fontSize: 14, flex: 1 },
  lockBadge: {
    color: theme.warning,
    fontSize: 10,
    borderWidth: 1,
    borderColor: theme.warning,
    borderRadius: radius.sm,
    paddingHorizontal: 6,
    paddingVertical: 1,
    overflow: 'hidden',
  },
  avatar: {
    width: 44,
    height: 44,
    borderRadius: 22,
    backgroundColor: theme.surfaceAlt,
    alignItems: 'center',
    justifyContent: 'center',
  },
  avatarText: { color: theme.text, fontSize: 18, fontWeight: '700' },
  empty: { padding: space.xl, alignItems: 'center', gap: space.sm },
  emptyText: { color: theme.text, fontSize: 16 },
  emptyHint: { color: theme.textMuted, fontSize: 13 },
});
