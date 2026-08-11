import { useEffect, useRef, useState } from 'react';
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
import QRCode from 'react-native-qrcode-svg';
import * as Clipboard from 'expo-clipboard';
import * as Wacli from 'expo-wacli';

import { errorMessage } from '../format';
import { radius, space, theme } from '../theme';

type Mode = 'phone' | 'qr';

export function LoginScreen({ onPaired }: { onPaired: () => void }) {
  const [mode, setMode] = useState<Mode>('phone');
  const [phone, setPhone] = useState('');
  const [pairingCode, setPairingCode] = useState<string | null>(null);
  const [qrCode, setQrCode] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // onPaired lands in an effect that must not re-subscribe when the parent re-renders, so it is
  // held in a ref rather than listed as a dependency.
  const onPairedRef = useRef(onPaired);
  onPairedRef.current = onPaired;

  useEffect(() => {
    const subscriptions = [
      Wacli.addListener('onLoginQRCode', ({ code }) => {
        setQrCode(code);
        setBusy(false);
      }),
      Wacli.addListener('onLoginPairingCode', ({ code }) => {
        setPairingCode(code);
        setBusy(false);
      }),
      Wacli.addListener('onLoginStatus', ({ status }) => {
        if (status === 'success') {
          setNote('Paired. Syncing your chats…');
          onPairedRef.current();
          return;
        }
        if (status === 'timeout') {
          setBusy(false);
          setQrCode(null);
          setPairingCode(null);
          setError('The code expired before it was used. Try again.');
        }
        if (status === 'connecting') {
          setNote('Contacting WhatsApp…');
        }
      }),
      Wacli.addListener('onLoginError', ({ message }) => {
        setBusy(false);
        setError(message);
      }),
    ];
    return () => subscriptions.forEach((subscription) => subscription.remove());
  }, []);

  // Cancel any attempt still running if the user navigates away mid-login, so the next attempt
  // does not collide with it.
  useEffect(() => () => void Wacli.cancelLogin().catch(() => {}), []);

  async function begin() {
    setError(null);
    setNote(null);
    setPairingCode(null);
    setQrCode(null);
    setBusy(true);
    try {
      if (mode === 'phone') {
        await Wacli.loginWithPhone(phone.trim());
      } else {
        await Wacli.loginWithQR();
      }
    } catch (e) {
      setBusy(false);
      setError(errorMessage(e));
    }
  }

  async function copyCode(code: string) {
    await Clipboard.setStringAsync(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }

  async function reset() {
    await Wacli.cancelLogin().catch(() => {});
    setPairingCode(null);
    setQrCode(null);
    setBusy(false);
    setError(null);
    setNote(null);
    setCopied(false);
  }

  const showingCode = pairingCode !== null || qrCode !== null;

  return (
    <KeyboardAvoidingView
      style={styles.flex}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView contentContainerStyle={styles.container}>
        <Text style={styles.title}>Link your WhatsApp</Text>
        <Text style={styles.subtitle}>
          wacli runs inside this app. Your session and messages stay on the device.
        </Text>

        {!showingCode && (
          <>
            <View style={styles.tabs}>
              <Tab label="Pairing code" active={mode === 'phone'} onPress={() => setMode('phone')} />
              <Tab label="QR code" active={mode === 'qr'} onPress={() => setMode('qr')} />
            </View>

            {mode === 'phone' ? (
              <>
                <Text style={styles.hint}>
                  Best choice on a phone: WhatsApp shows you a field to type a code into, so nothing
                  needs to be scanned.
                </Text>
                <TextInput
                  style={styles.input}
                  value={phone}
                  onChangeText={setPhone}
                  placeholder="+15551234567"
                  placeholderTextColor={theme.textMuted}
                  keyboardType="phone-pad"
                  autoCorrect={false}
                  editable={!busy}
                />
              </>
            ) : (
              <Text style={styles.hint}>
                Shows a QR for another device to scan. If WhatsApp is on this phone, use the pairing
                code instead — there is nothing here to scan it with.
              </Text>
            )}

            <Pressable
              style={({ pressed }) => [
                styles.button,
                (busy || (mode === 'phone' && !phone.trim())) && styles.buttonDisabled,
                pressed && styles.buttonPressed,
              ]}
              disabled={busy || (mode === 'phone' && !phone.trim())}
              onPress={begin}
            >
              {busy ? (
                <ActivityIndicator color={theme.accentText} />
              ) : (
                <Text style={styles.buttonText}>
                  {mode === 'phone' ? 'Get pairing code' : 'Show QR code'}
                </Text>
              )}
            </Pressable>
          </>
        )}

        {pairingCode && (
          <View style={styles.codeCard}>
            <Text style={styles.codeLabel}>Enter this in WhatsApp</Text>
            {/*
              Tapping to copy matters more here than it looks: the user is about to switch to
              WhatsApp and type eight characters from memory, and this is the same phone, so there
              is no second screen to read them off.
            */}
            <Pressable onPress={() => copyCode(pairingCode)}>
              <Text style={styles.code}>{formatPairingCode(pairingCode)}</Text>
            </Pressable>
            <Text style={styles.copyHint}>{copied ? 'Copied' : 'Tap the code to copy'}</Text>
            <Text style={styles.codeSteps}>
              WhatsApp → Settings → Linked Devices → Link a device → Link with phone number instead
            </Text>
          </View>
        )}

        {qrCode && (
          <View style={styles.codeCard}>
            <Text style={styles.codeLabel}>Scan from another device</Text>
            <View style={styles.qrFrame}>
              <QRCode value={qrCode} size={220} backgroundColor="#ffffff" color="#000000" />
            </View>
            <Text style={styles.codeSteps}>
              WhatsApp → Settings → Linked Devices → Link a device
            </Text>
          </View>
        )}

        {note && <Text style={styles.note}>{note}</Text>}
        {error && <Text style={styles.error}>{error}</Text>}

        {showingCode && (
          <Pressable style={styles.secondaryButton} onPress={reset}>
            <Text style={styles.secondaryButtonText}>Start over</Text>
          </Pressable>
        )}

        <Text style={styles.disclaimer}>
          Automating a personal WhatsApp account is against WhatsApp&apos;s Terms of Service and can
          get the number banned. Use a number you control and accept that risk for.
        </Text>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

function Tab({
  label,
  active,
  onPress,
}: {
  label: string;
  active: boolean;
  onPress: () => void;
}) {
  return (
    <Pressable style={[styles.tab, active && styles.tabActive]} onPress={onPress}>
      <Text style={[styles.tabText, active && styles.tabTextActive]}>{label}</Text>
    </Pressable>
  );
}

/** WhatsApp displays the eight-character code in two groups; match that so it is easy to copy. */
function formatPairingCode(code: string): string {
  return code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}

const styles = StyleSheet.create({
  flex: { flex: 1 },
  container: {
    padding: space.xl,
    gap: space.lg,
    justifyContent: 'center',
    flexGrow: 1,
  },
  title: { color: theme.text, fontSize: 28, fontWeight: '700' },
  subtitle: { color: theme.textMuted, fontSize: 15, lineHeight: 21 },
  tabs: { flexDirection: 'row', gap: space.sm },
  tab: {
    flex: 1,
    paddingVertical: space.md,
    borderRadius: radius.md,
    backgroundColor: theme.surface,
    borderWidth: 1,
    borderColor: theme.border,
    alignItems: 'center',
  },
  tabActive: { backgroundColor: theme.surfaceAlt, borderColor: theme.accent },
  tabText: { color: theme.textMuted, fontWeight: '600' },
  tabTextActive: { color: theme.text },
  hint: { color: theme.textMuted, fontSize: 13, lineHeight: 19 },
  input: {
    backgroundColor: theme.surface,
    borderWidth: 1,
    borderColor: theme.border,
    borderRadius: radius.md,
    padding: space.lg,
    color: theme.text,
    fontSize: 18,
  },
  button: {
    backgroundColor: theme.accent,
    borderRadius: radius.md,
    padding: space.lg,
    alignItems: 'center',
    minHeight: 52,
    justifyContent: 'center',
  },
  buttonDisabled: { opacity: 0.4 },
  buttonPressed: { opacity: 0.85 },
  buttonText: { color: theme.accentText, fontWeight: '700', fontSize: 16 },
  secondaryButton: { padding: space.md, alignItems: 'center' },
  secondaryButtonText: { color: theme.textMuted, fontWeight: '600' },
  codeCard: {
    backgroundColor: theme.surface,
    borderRadius: radius.lg,
    borderWidth: 1,
    borderColor: theme.border,
    padding: space.xl,
    alignItems: 'center',
    gap: space.md,
  },
  codeLabel: { color: theme.textMuted, fontSize: 13, textTransform: 'uppercase', letterSpacing: 1 },
  code: {
    color: theme.text,
    fontSize: 40,
    fontWeight: '700',
    letterSpacing: 4,
    fontVariant: ['tabular-nums'],
  },
  copyHint: { color: theme.accent, fontSize: 12 },
  codeSteps: { color: theme.textMuted, fontSize: 13, textAlign: 'center', lineHeight: 19 },
  qrFrame: { backgroundColor: '#ffffff', padding: space.lg, borderRadius: radius.md },
  note: { color: theme.accent, textAlign: 'center' },
  error: { color: theme.danger, textAlign: 'center', lineHeight: 20 },
  disclaimer: {
    color: theme.textMuted,
    fontSize: 11,
    lineHeight: 16,
    textAlign: 'center',
    marginTop: space.lg,
  },
});
