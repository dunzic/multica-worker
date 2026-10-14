"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { useConversationChange, type Conversation, type Principal } from "@multica/core/chat-v2";
import { useActorName } from "@multica/core/workspace/hooks";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";

export function GroupInfoDialog({ conversation, onLeft }: { conversation: Conversation; onLeft: () => void }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id);
  const { getActorName } = useActorName();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const change = useConversationChange(wsId);
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState(conversation.title ?? "");
  const [choice, setChoice] = useState("");
  const [error, setError] = useState(false);
  const [confirmation, setConfirmation] = useState<{ action: "leave" } | { action: "remove"; principal: Principal } | null>(null);
  const myRole = conversation.participants.find((p) => p.principal_type === "member" && p.principal_id === userId)?.role;
  const isAdmin = myRole === "admin";
  const lastAdmin = isAdmin && conversation.participants.filter((p) => p.principal_type === "member" && p.role === "admin").length === 1;
  const present = new Set(conversation.participants.map((p) => p.principal_type + ":" + p.principal_id));
  const choices = [
    ...members.map((m) => ({ type: "member" as const, id: m.user_id, name: m.name })),
    ...agents.filter((a) => !a.archived_at).map((a) => ({ type: "agent" as const, id: a.id, name: a.name })),
  ].filter((p) => !present.has(p.type + ":" + p.id));
  const run = async (data: Parameters<typeof change.mutateAsync>[0]) => {
    setError(false);
    try {
      await change.mutateAsync(data);
      if (data.action === "leave" || data.action === "remove" && data.principal.type === "member" && data.principal.id === userId) { setOpen(false); onLeft(); }
      return true;
    } catch { setError(true); return false; }
  };
  return <>
    <Dialog open={open} onOpenChange={(value) => { setOpen(value); if (value) { setTitle(conversation.title ?? ""); setError(false); } }}>
      <DialogTrigger render={<Button variant="outline" />}>{t(($) => $.groups.info)}</DialogTrigger>
      <DialogContent>
        <DialogHeader><DialogTitle>{t(($) => $.groups.info)}</DialogTitle></DialogHeader>
        {isAdmin && <div className="flex items-end gap-2">
          <label className="min-w-0 flex-1 text-body">{t(($) => $.groups.name)}<Input value={title} maxLength={255} disabled={change.isPending} onChange={(e) => setTitle(e.target.value)} /></label>
          <Button disabled={change.isPending || !title.trim()} onClick={() => run({ action: "rename", id: conversation.id, title: title.trim() })}>{t(($) => $.groups.save)}</Button>
        </div>}
        <ul className="max-h-64 overflow-y-auto">
          {conversation.participants.map((p) => <li key={p.principal_type + p.principal_id} className="flex flex-wrap items-center gap-2 py-1 text-body">
            <span className="min-w-0 flex-1 truncate">{getActorName(p.principal_type, p.principal_id)} · {p.role}</span>
            {isAdmin && p.principal_type === "member" && p.role !== "admin" && <Button size="xs" variant="outline" disabled={change.isPending} onClick={() => run({ action: "add", id: conversation.id, principal: { type: "member", id: p.principal_id }, role: "admin" })}>{t(($) => $.groups.promote)}</Button>}
            {isAdmin && <Button size="xs" variant="ghost" disabled={change.isPending || lastAdmin && p.principal_type === "member" && p.principal_id === userId} onClick={() => setConfirmation({ action: "remove", principal: { type: p.principal_type, id: p.principal_id } })}>{t(($) => $.groups.remove)}</Button>}
          </li>)}
        </ul>
        {isAdmin && <div className="flex items-end gap-2">
          <label className="min-w-0 flex-1 text-body">{t(($) => $.groups.invite)}<select className="w-full rounded-md border border-input bg-background p-2 text-body" value={choice} disabled={change.isPending} onChange={(e) => setChoice(e.target.value)}>
            <option value="">{t(($) => $.groups.select_participant)}</option>
            {choices.map((p) => <option key={p.type + p.id} value={p.type + ":" + p.id}>{p.name}</option>)}
          </select></label>
          <Button disabled={change.isPending || !choice} onClick={async () => {
            const principal = choices.find((p) => p.type + ":" + p.id === choice);
            if (principal && await run({ action: "add", id: conversation.id, principal: { type: principal.type, id: principal.id } })) setChoice("");
          }}>{t(($) => $.groups.invite)}</Button>
        </div>}
        {lastAdmin && <p className="text-caption text-muted-foreground">{t(($) => $.groups.last_admin_hint)}</p>}
        {error && <p role="alert" className="text-body text-destructive">{t(($) => $.groups.change_failed)}</p>}
        <DialogFooter><Button variant="outline" disabled={change.isPending || lastAdmin} onClick={() => setConfirmation({ action: "leave" })}>{t(($) => $.groups.leave)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
    <Dialog open={confirmation !== null} onOpenChange={(value) => { if (!value && !change.isPending) setConfirmation(null); }}>
      <DialogContent>
        <DialogHeader><DialogTitle>{t(($) => $.groups.confirm_change)}</DialogTitle><DialogDescription>{t(($) => $.groups.removal_hint)}</DialogDescription></DialogHeader>
        <DialogFooter>
          <Button variant="outline" disabled={change.isPending} onClick={() => setConfirmation(null)}>{t(($) => $.groups.cancel)}</Button>
          <Button variant="destructive" disabled={change.isPending} onClick={async () => {
            if (confirmation && await run({ ...confirmation, id: conversation.id })) setConfirmation(null);
          }}>{t(($) => $.groups.confirm_change)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </>;
}
