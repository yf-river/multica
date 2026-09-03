import { useState } from "react";
import { KeyboardAvoidingView, Platform, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import * as Haptics from "expo-haptics";
import { Text } from "@/components/ui/text";
import { TextField } from "@/components/ui/text-field";
import { Button } from "@/components/ui/button";
import { MulticaLogo } from "@/components/brand/multica-logo";
import { useAuthStore } from "@/data/auth-store";
import { mapAuthError } from "@/lib/auth-error";

export default function Login() {
  const login = useAuthStore((s) => s.login);
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onSubmit = async () => {
    const trimmed = account.trim();
    if (!trimmed || !password) return;
    void Haptics.selectionAsync();
    setSubmitting(true);
    setError(null);
    try {
      await login(trimmed, password);
      router.replace("/");
    } catch (err) {
      void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Error);
      setError(mapAuthError(err, "Couldn't send the code. Try again."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <SafeAreaView className="flex-1 bg-background">
      <KeyboardAvoidingView
        className="flex-1"
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <View className="flex-1 justify-center px-6 gap-6">
          <View className="items-center gap-3">
            <MulticaLogo size={32} />
            <View className="gap-1 items-center">
              <Text className="text-2xl font-semibold text-foreground">
                Sign in to Multica
              </Text>
              <Text className="text-sm text-muted-foreground text-center">Enter your account and password.</Text>
            </View>
          </View>

          <View className="gap-3">
            <TextField
              autoCapitalize="none"
              autoComplete="username"
              autoFocus
              placeholder="Account"
              value={account}
              onChangeText={setAccount}
              onSubmitEditing={onSubmit}
              returnKeyType="next"
              editable={!submitting}
              invalid={!!error}
            />
            {error ? (
              <Text className="text-sm text-destructive">{error}</Text>
            ) : null}
          </View>

          <TextField secureTextEntry placeholder="Password" value={password} onChangeText={setPassword} editable={!submitting} invalid={!!error} />
          <Button
            size="lg"
            disabled={submitting || !account.trim() || !password}
            onPress={onSubmit}
          >
            <Text>{submitting ? "Signing in..." : "Sign in"}</Text>
          </Button>
        </View>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
