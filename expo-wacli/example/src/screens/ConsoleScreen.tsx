import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import * as Wacli from 'expo-wacli';

import { errorMessage } from '../format';
import { radius, space, theme } from '../theme';

/**
 * A terminal for wacli.
 *
 * The commands are not reimplemented here — `Wacli.exec` runs the same command layer the `wacli`
 * binary runs, so whatever the CLI reference documents works verbatim, and anything added to the
 * CLI later shows up here without this screen changing.
 */

type Line =
  | { id: number; kind: 'input'; text: string }
  | { id: number; kind: 'output'; text: string }
  | { id: number; kind: 'note'; text: string };

const BANNER = [
  'wacli console',
  '',
  "Runs the real client commands. Type 'help' for the list, 'clear' to reset.",
  'Arguments split shell-style: quote anything containing spaces.',
].join('\n');

// Enough to make the first tap useful without turning into a menu.
const QUICK = ['status', 'chats --limit 10', 'dnd', 'calls', 'triggers list'];

let nextID = 0;

export function ConsoleScreen() {
  const [lines, setLines] = useState<Line[]>([{ id: nextID++, kind: 'note', text: BANNER }]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [commands, setCommands] = useState<string[]>([]);
  const [history, setHistory] = useState<string[]>([]);
  const scrollRef = useRef<ScrollView>(null);

  useEffect(() => {
    // Sourced from the binary rather than hardcoded, so the completions cannot drift from reality.
    Wacli.execCommands()
      .then(setCommands)
      .catch(() => setCommands([]));
  }, []);

  const append = useCallback((entries: Line[]) => {
    setLines((prev) => [...prev, ...entries]);
  }, []);

  const run = useCallback(
    async (raw: string) => {
      const line = raw.trim();
      if (!line || busy) return;

      setInput('');
      setHistory((prev) => [line, ...prev.filter((h) => h !== line)].slice(0, 50));

      // Two conveniences that belong to the console rather than to wacli, so they never reach exec.
      if (line === 'clear') {
        setLines([]);
        return;
      }
      if (line === 'help') {
        append([
          { id: nextID++, kind: 'input', text: line },
          {
            id: nextID++,
            kind: 'note',
            text:
              commands.length > 0
                ? `commands:\n  ${commands.join('\n  ')}\n\nRun any of them with --help for flags.`
                : 'Command list unavailable — is wacli running?',
          },
        ]);
        return;
      }

      append([{ id: nextID++, kind: 'input', text: line }]);
      setBusy(true);
      try {
        const output = await Wacli.exec(line);
        append([
          { id: nextID++, kind: 'output', text: output.trimEnd() || '(no output)' },
        ]);
      } catch (e) {
        // exec only rejects when the line could not run at all; a command that ran and failed
        // reports inside its own output.
        append([{ id: nextID++, kind: 'output', text: errorMessage(e) }]);
      } finally {
        setBusy(false);
      }
    },
    [append, busy, commands]
  );

  // Complete only the command word, and only while it is still being typed.
  const suggestions = useMemo(() => {
    const trimmed = input.trimStart();
    if (!trimmed || trimmed.includes(' ')) return [];
    return commands.filter((c) => c.startsWith(trimmed) && c !== trimmed).slice(0, 6);
  }, [commands, input]);

  const chips = input.trim() === '' ? (history.length > 0 ? history.slice(0, 6) : QUICK) : [];

  return (
    <KeyboardAvoidingView
      style={styles.root}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      keyboardVerticalOffset={Platform.OS === 'ios' ? 0 : 0}
    >
      <ScrollView
        ref={scrollRef}
        style={styles.scroll}
        contentContainerStyle={styles.scrollContent}
        onContentSizeChange={() => scrollRef.current?.scrollToEnd({ animated: true })}
        keyboardShouldPersistTaps="handled"
      >
        {lines.map((line) => {
          if (line.kind === 'input') {
            return (
              <Pressable key={line.id} onPress={() => setInput(line.text)}>
                <Text style={styles.inputLine} selectable>
                  <Text style={styles.prompt}>$ </Text>
                  {line.text}
                </Text>
              </Pressable>
            );
          }
          return (
            <Text
              key={line.id}
              style={line.kind === 'note' ? styles.noteLine : styles.outputLine}
              selectable
            >
              {line.text}
            </Text>
          );
        })}
        {busy && <ActivityIndicator style={styles.busy} color={theme.accent} />}
      </ScrollView>

      {(suggestions.length > 0 || chips.length > 0) && (
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={styles.chipBar}
          contentContainerStyle={styles.chipBarContent}
          keyboardShouldPersistTaps="always"
        >
          {(suggestions.length > 0 ? suggestions : chips).map((value) => (
            <Pressable
              key={value}
              style={styles.chip}
              onPress={() =>
                suggestions.length > 0 ? setInput(value + ' ') : run(value)
              }
            >
              <Text style={styles.chipText}>{value}</Text>
            </Pressable>
          ))}
        </ScrollView>
      )}

      <View style={styles.inputRow}>
        <Text style={styles.prompt}>$</Text>
        <TextInput
          style={styles.textInput}
          value={input}
          onChangeText={setInput}
          onSubmitEditing={() => run(input)}
          placeholder="status"
          placeholderTextColor={theme.textMuted}
          autoCapitalize="none"
          autoCorrect={false}
          autoComplete="off"
          spellCheck={false}
          returnKeyType="send"
          blurOnSubmit={false}
          editable={!busy}
        />
        <Pressable
          style={[styles.runButton, (busy || !input.trim()) && styles.runButtonDisabled]}
          onPress={() => run(input)}
          disabled={busy || !input.trim()}
        >
          <Text style={styles.runButtonText}>Run</Text>
        </Pressable>
      </View>
    </KeyboardAvoidingView>
  );
}

const mono = Platform.select({ ios: 'Menlo', android: 'monospace', default: 'monospace' });

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: theme.bg },
  scroll: { flex: 1 },
  scrollContent: { padding: space.md, gap: space.xs },
  prompt: { color: theme.accent, fontFamily: mono, fontWeight: '700' },
  inputLine: { color: theme.text, fontFamily: mono, fontSize: 13, lineHeight: 19 },
  outputLine: { color: theme.textMuted, fontFamily: mono, fontSize: 12, lineHeight: 18 },
  noteLine: { color: theme.warning, fontFamily: mono, fontSize: 12, lineHeight: 18 },
  busy: { alignSelf: 'flex-start', marginVertical: space.sm },

  chipBar: { maxHeight: 44, borderTopWidth: 1, borderTopColor: theme.border },
  chipBarContent: { padding: space.sm, gap: space.sm },
  chip: {
    backgroundColor: theme.surfaceAlt,
    paddingHorizontal: space.md,
    paddingVertical: space.xs,
    borderRadius: radius.sm,
  },
  chipText: { color: theme.text, fontFamily: mono, fontSize: 12 },

  inputRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: space.sm,
    padding: space.md,
    borderTopWidth: 1,
    borderTopColor: theme.border,
    backgroundColor: theme.surface,
  },
  textInput: {
    flex: 1,
    color: theme.text,
    fontFamily: mono,
    fontSize: 13,
    paddingVertical: space.sm,
  },
  runButton: {
    backgroundColor: theme.accent,
    paddingHorizontal: space.lg,
    paddingVertical: space.sm,
    borderRadius: radius.sm,
  },
  runButtonDisabled: { opacity: 0.4 },
  runButtonText: { color: theme.accentText, fontWeight: '700' },
});
