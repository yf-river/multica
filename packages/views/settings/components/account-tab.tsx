// 本文件是「我的账户」标签页：编辑头像、昵称和个人简介，带自动保存。

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { AvatarUploadControl } from "../../common/avatar-upload-control";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";
import { useAutoSave } from "./use-auto-save";

// 与服务端 server/internal/handler/auth.go:MaxProfileDescriptionLen 保持一致。
// 这里用 JS 的 String.length（UTF-16 码元）计数，而服务端按 rune 计数，
// 所以一个充满扩展平面 emoji 的简介会先触发客户端上限、再触发服务端上限——这是更安全的偏差方向。
const MAX_PROFILE_DESCRIPTION_LEN = 2000;

/** 个人资料草稿：昵称和简介。 */
interface ProfileDraft {
  name: string;
  profileDescription: string;
}

/** 判断两份资料草稿是否相同，用于自动保存时判断有没有改动。 */
function profilesEqual(left: ProfileDraft, right: ProfileDraft) {
  return left.name === right.name && left.profileDescription === right.profileDescription;
}

/**
 * 「我的账户」标签页：展示并编辑头像、昵称、个人简介，改动后自动保存到服务端。
 */
export function AccountTab() {
  const { t } = useT("settings");
  const user = useAuthStore((s) => s.user);
  const setUser = useAuthStore((s) => s.setUser);

  const [profileName, setProfileName] = useState(user?.name ?? "");
  const [profileDescription, setProfileDescription] = useState(
    user?.profile_description ?? "",
  );

  useEffect(() => {
    setProfileName(user?.name ?? "");
    setProfileDescription(user?.profile_description ?? "");
    // 当头像上传或自动保存替换了 auth store 里的用户对象时，保留正在编辑中的内容。
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 有意只按用户身份（id）触发
  }, [user?.id]);

  const descriptionTooLong = profileDescription.length > MAX_PROFILE_DESCRIPTION_LEN;

  const draft = useMemo(
    () => ({ name: profileName, profileDescription }),
    [profileDescription, profileName],
  );
  const savedDraft = useMemo(
    () => ({
      name: user?.name ?? "",
      profileDescription: user?.profile_description ?? "",
    }),
    [user?.name, user?.profile_description],
  );
  const saveProfile = useCallback(
    async (next: ProfileDraft) => {
      const updated = await api.updateMe({
        name: next.name,
        profile_description: next.profileDescription,
      });
      setUser(updated);
    },
    [setUser],
  );
  const autoSave = useAutoSave({
    value: draft,
    savedValue: savedDraft,
    onSave: saveProfile,
    onSuccess: () =>
      toast.success(t(($) => $.account.toast_profile_updated), {
        id: "settings-auto-save",
      }),
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.account.toast_profile_failed),
      ),
    enabled: !!user && !!profileName.trim() && !descriptionTooLong,
    isEqual: profilesEqual,
  });

  return (
    <SettingsTab title={t(($) => $.page.tabs.profile)}>
      <SettingsSection
        title={t(($) => $.account.section_profile)}
        action={
          <SettingsSaveState
            status={autoSave.status}
            savingLabel={t(($) => $.auto_save.saving)}
            savedLabel={t(($) => $.auto_save.saved)}
            errorLabel={t(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.account.avatar_label)}
            description={t(($) => $.account.click_avatar_hint)}
            size="none"
          >
            <div className="flex justify-start sm:justify-end">
              <AvatarUploadControl
                variant="user"
                value={user?.avatar_url ?? null}
                name={user?.name ?? ""}
                size={64}
                onUploaded={async (url) => {
                  try {
                    const updated = await api.updateMe({ avatar_url: url });
                    setUser(updated);
                    toast.success(t(($) => $.account.toast_avatar_updated), {
                      id: "settings-auto-save",
                    });
                  } catch (error) {
                    toast.error(
                      error instanceof Error
                        ? error.message
                        : t(($) => $.account.toast_avatar_failed),
                    );
                  }
                }}
              />
            </div>
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.account.name_label)}
            size="text"
          >
            <Input
              type="text"
              name="profile-name"
              autoComplete="name"
              aria-label={t(($) => $.account.name_label)}
              value={profileName}
              onChange={(event) => setProfileName(event.target.value)}
              onBlur={autoSave.flush}
            />
          </SettingsRow>

          <SettingsRow
            label={t(($) => $.account.profile_description_label)}
            description={t(($) => $.account.profile_description_hint)}
            size="text"
            align="start"
          >
            <div>
              <Textarea
                name="profile-description"
                autoComplete="off"
                aria-label={t(($) => $.account.profile_description_label)}
                value={profileDescription}
                onChange={(event) => setProfileDescription(event.target.value)}
                onBlur={autoSave.flush}
                placeholder={t(($) => $.account.profile_description_placeholder)}
                rows={5}
                maxLength={MAX_PROFILE_DESCRIPTION_LEN}
                aria-invalid={descriptionTooLong}
                className="resize-y"
              />
              <div className="mt-1 flex justify-end text-caption text-muted-foreground">
                <span
                  className={descriptionTooLong ? "text-destructive shrink-0" : "shrink-0"}
                  aria-live="polite"
                >
                  {profileDescription.length}/{MAX_PROFILE_DESCRIPTION_LEN}
                </span>
              </div>
              {descriptionTooLong ? (
                <p className="mt-1 text-caption text-destructive">
                  {t(($) => $.account.profile_description_too_long, {
                    max: MAX_PROFILE_DESCRIPTION_LEN,
                    count: profileDescription.length,
                  })}
                </p>
              ) : null}
            </div>
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
