import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import * as Wacli from 'expo-wacli';
import type { ChatRecord, MessageRecord } from 'expo-wacli';

import { displayName, errorMessage, formatTime } from '../format';
import { radius, space, theme } from '../theme';

export function ChatScreen({ chat, onBack }: { chat: ChatRecord; onBack: () => void }) {
  const [messages, setMessages] = useState<MessageRecord[]>([]);
  const [draft, setDraft] = useState('');
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const chatJID = chat.jid;

  const load = useCallback(async () => {
    try {
      const records = await Wacli.listMessages({ chat: chatJID, limit: 100 });
      // wacli returns newest-first; the list renders inverted, which wants the same order.
      setMessages(records);
      setError(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setLoading(false);
    }
  }, [chatJID]);

  useEffect(() => {
    load();
    const subscription = Wacli.addListener('onEvent', ({ event, payload }) => {
      if (event !== 'incoming_message' && event !== 'outgoing_message') {
        return;
      }
      // Only reload for this conversation — every other chat's traffic is noise here.
      if (payload.message?.chat_jid === chatJID) {
        load();
      }
    });
    return () => subscription.remove();
  }, [chatJID, load]);

  const send = useCallback(async () => {
    const text = draft.trim();
    if (!text || sending) {
      return;
    }
    setSending(true);
    setError(null);
    try {
      await Wacli.sendMessage({ to: chatJID, text });
      setDraft('');
      await load();
    } catch (e) {
      // wacli refuses sends when DND is off or the chat is locked. Both are deliberate gates, so
      // show what it said rather than a generic failure.
      setError(errorMessage(e));
    } finally {
      setSending(false);
    }
  }, [chatJID, draft, load, sending]);

  const listRef = useRef<FlatList<MessageRecord>>(null);

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <View style={styles.header}>
        <Pressable onPress={onBack} hitSlop={12} style={styles.back}>
          <Text style={styles.backText}>‹</Text>
        </Pressable>
        <View style={styles.flex}>
          <Text style={styles.title} numberOfLines={1}>
            {displayName(chat.name, chat.jid)}
          </Text>
          <Text style={styles.subtitle}>
            {chat.is_group ? 'Group' : 'Direct'}
            {chat.locked ? ' · locked to automation' : ''}
          </Text>
        </View>
      </View>

      {loading ? (
        <View style={styles.centered}>
          <ActivityIndicator color={theme.accent} />
        </View>
      ) : (
        <FlatList
          ref={listRef}
          data={messages}
          inverted
          keyExtractor={(message) => message.id}
          contentContainerStyle={styles.list}
          ListEmptyComponent={
            <View style={styles.centered}>
              <Text style={styles.emptyText}>No messages in this chat yet.</Text>
            </View>
          }
          renderItem={({ item }) => <Bubble message={item} />}
        />
      )}

      {error && (
        <View style={styles.errorBar}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      <View style={styles.composer}>
        <TextInput
          style={styles.input}
          value={draft}
          onChangeText={setDraft}
          placeholder={chat.locked ? 'This chat is locked' : 'Message'}
          placeholderTextColor={theme.textMuted}
          editable={!chat.locked}
          multiline
        />
        <Pressable
          style={({ pressed }) => [
            styles.send,
            (!draft.trim() || sending || chat.locked) && styles.sendDisabled,
            pressed && styles.sendPressed,
          ]}
          disabled={!draft.trim() || sending || chat.locked}
          onPress={send}
        >
          {sending ? (
            <ActivityIndicator color={theme.accentText} size="small" />
          ) : (
            <Text style={styles.sendText}>Send</Text>
          )}
        </Pressable>
      </View>
    </KeyboardAvoidingView>
  );
}

function Bubble({ message }: { message: MessageRecord }) {
  const mine = message.is_from_me;
  const hasMedia = Boolean(message.media_type);
  return (
    <View style={[styles.bubbleRow, mine ? styles.bubbleRowMine : styles.bubbleRowTheirs]}>
      <View style={[styles.bubble, mine ? styles.bubbleMine : styles.bubbleTheirs]}>
        {hasMedia && (
          <Text style={styles.mediaTag}>
            {message.media_type}
            {message.file_name ? ` · ${message.file_name}` : ''}
          </Text>
        )}
        {Boolean(message.content) && <Text style={styles.bubbleText}>{message.content}</Text>}
        <View style={styles.bubbleMeta}>
          {message.mentions_me && <Text style={styles.mentionTag}>mentioned you</Text>}
          <Text style={styles.bubbleTime}>{formatTime(message.timestamp)}</Text>
        </View>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  centered: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: space.xl },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: space.sm,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: theme.border,
  },
  back: { paddingHorizontal: space.sm },
  backText: { color: theme.accent, fontSize: 32, lineHeight: 34 },
  title: { color: theme.text, fontSize: 17, fontWeight: '600' },
  subtitle: { color: theme.textMuted, fontSize: 12 },
  list: { padding: space.md, gap: space.sm },
  emptyText: { color: theme.textMuted, transform: [{ scaleY: -1 }] },
  bubbleRow: { flexDirection: 'row' },
  bubbleRowMine: { justifyContent: 'flex-end' },
  bubbleRowTheirs: { justifyContent: 'flex-start' },
  bubble: {
    maxWidth: '82%',
    borderRadius: radius.lg,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    gap: space.xs,
  },
  bubbleMine: { backgroundColor: theme.bubbleMine, borderBottomRightRadius: radius.sm },
  bubbleTheirs: { backgroundColor: theme.bubbleTheirs, borderBottomLeftRadius: radius.sm },
  bubbleText: { color: theme.text, fontSize: 15, lineHeight: 21 },
  bubbleMeta: { flexDirection: 'row', alignItems: 'center', gap: space.sm, alignSelf: 'flex-end' },
  bubbleTime: { color: theme.textMuted, fontSize: 11 },
  mentionTag: { color: theme.warning, fontSize: 11 },
  mediaTag: { color: theme.textMuted, fontSize: 11, fontStyle: 'italic' },
  errorBar: {
    backgroundColor: theme.surfaceAlt,
    borderTopWidth: 1,
    borderTopColor: theme.danger,
    padding: space.md,
  },
  errorText: { color: theme.danger, fontSize: 13 },
  composer: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    gap: space.sm,
    padding: space.md,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: theme.border,
  },
  input: {
    flex: 1,
    maxHeight: 120,
    backgroundColor: theme.surface,
    borderWidth: 1,
    borderColor: theme.border,
    borderRadius: radius.lg,
    paddingHorizontal: space.md,
    paddingVertical: space.sm,
    color: theme.text,
    fontSize: 15,
  },
  send: {
    backgroundColor: theme.accent,
    borderRadius: radius.lg,
    paddingHorizontal: space.lg,
    paddingVertical: space.md,
    minWidth: 76,
    alignItems: 'center',
  },
  sendDisabled: { opacity: 0.4 },
  sendPressed: { opacity: 0.85 },
  sendText: { color: theme.accentText, fontWeight: '700' },
});
