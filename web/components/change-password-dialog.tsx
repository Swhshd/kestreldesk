"use client";

import { useEffect, useMemo } from "react";
import { zodResolver } from "@hookform/resolvers/zod";
import { Resolver, useForm } from "react-hook-form";
import { z } from "zod/v4";
import { toast } from "sonner";

import { changeSelfPassword } from "@/lib/api/admin";
import { useI18n } from "@/i18n/provider";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldContent,
  FieldError,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ProjectDialog } from "@/components/project-dialog";

function createChangePasswordSchema(t: (key: string) => string) {
  return z
    .object({
      password: z.string().trim().min(1, t("account.passwordRequired")),
      confirmPassword: z.string().trim().min(1, t("account.confirmPasswordRequired")),
    })
    .refine((data) => data.password === data.confirmPassword, {
      path: ["confirmPassword"],
      message: t("account.passwordMismatch"),
    });
}

type ChangePasswordForm = {
  password: string;
  confirmPassword: string;
};

const emptyForm: ChangePasswordForm = {
  password: "",
  confirmPassword: "",
};

type ChangePasswordDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSuccess: () => Promise<void>;
};

export function ChangePasswordDialog({
  open,
  onOpenChange,
  onSuccess,
}: ChangePasswordDialogProps) {
  const t = useI18n();
  const changePasswordSchema = useMemo(() => createChangePasswordSchema(t), [t]);
  const changePasswordResolver = useMemo(
    () =>
      zodResolver(changePasswordSchema as never) as Resolver<
        z.input<typeof changePasswordSchema>,
        undefined,
        z.output<typeof changePasswordSchema>
      >,
    [changePasswordSchema],
  );
  const form = useForm<
    z.input<typeof changePasswordSchema>,
    undefined,
    z.output<typeof changePasswordSchema>
  >({
    resolver: changePasswordResolver,
    defaultValues: emptyForm,
  });
  const {
    handleSubmit,
    register,
    reset,
    formState: { errors, isSubmitting },
  } = form;

  useEffect(() => {
    if (open) {
      reset(emptyForm);
    }
  }, [open, reset]);

  async function onSubmit(values: ChangePasswordForm) {
    try {
      await changeSelfPassword(values.password.trim());
      toast.success(t("account.passwordChanged"));
      onOpenChange(false);
      await onSuccess();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("account.changePasswordFailed"));
    }
  }

  return (
    <ProjectDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("account.changePassword")}
      description={t("account.changePasswordDescription")}
      size="sm"
      allowFullscreen
      footer={
        <>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={isSubmitting}
          >
            {t("account.cancel")}
          </Button>
          <Button type="submit" form="change-password-form" disabled={isSubmitting}>
            {isSubmitting ? t("account.submitting") : t("account.confirmChange")}
          </Button>
        </>
      }
    >
      <form id="change-password-form" onSubmit={handleSubmit(onSubmit)}>
        <div className="space-y-4">
          <Field data-invalid={!!errors.password}>
            <FieldLabel htmlFor="change-password-password">{t("account.newPassword")}</FieldLabel>
            <FieldContent>
              <Input
                id="change-password-password"
                type="password"
                placeholder={t("account.newPasswordPlaceholder")}
                autoComplete="new-password"
                aria-invalid={!!errors.password}
                {...register("password")}
              />
              <FieldError errors={[errors.password]} />
            </FieldContent>
          </Field>
          <Field data-invalid={!!errors.confirmPassword}>
            <FieldLabel htmlFor="change-password-confirm">{t("account.confirmPassword")}</FieldLabel>
            <FieldContent>
              <Input
                id="change-password-confirm"
                type="password"
                placeholder={t("account.confirmPasswordPlaceholder")}
                autoComplete="new-password"
                aria-invalid={!!errors.confirmPassword}
                {...register("confirmPassword")}
              />
              <FieldError errors={[errors.confirmPassword]} />
            </FieldContent>
          </Field>
        </div>
      </form>
    </ProjectDialog>
  );
}
