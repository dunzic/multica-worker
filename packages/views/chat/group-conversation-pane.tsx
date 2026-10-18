"use client";

import { useEffect, useState, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { CHAT_ATTACHMENT_LIMITS } from "@multica/core/constants/upload";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspacePaths } from "@multica/core/paths";
import { conversationsOptions, topicsOptions, topicMessagesOptions, topicRunsOptions, isTopicRunActive, useCancelTopicRun, useCreateConversation, useCreateTopic, useSendTopicMessage } from "@multica/core/chat-v2";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import type { Principal } from "@multica/core/chat-v2";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { AppLink, useNavigation } from "../navigation";
import { ReadonlyContent } from "../editor";
import { Attachment } from "../editor/attachment";
import { ChatInput } from "./components/chat-input";
import { CHAT_COLUMN, CHAT_GUTTER } from "./components/chat-column";
import { GroupInfoDialog } from "./group-info-dialog";

export function GroupConversationPane() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { pathname, searchParams, replace, push } = useNavigation();
  const userId = useAuthStore((s) => s.user?.id);
  const listQuery = useQuery(conversationsOptions(wsId));
  const { data: conversations = [], error: listError } = listQuery;
  const id = searchParams.get("conversation") ?? "";
  const selectedIdRef = useRef(id);
  selectedIdRef.current = id;
  useEffect(() => () => { selectedIdRef.current = ""; }, []);
  const conversation = !listError ? conversations.find((item) => item.workspace_id === wsId && item.id === id && item.kind === "group") : undefined;
  const topicsQuery = useQuery(topicsOptions(wsId, conversation?.id ?? ""));
  const { data: topics = [], error: topicError, isSuccess: topicsLoaded } = topicsQuery;
  const requestedTopicId = searchParams.get("topic");
  const selectionRef = useRef("");
  selectionRef.current = `${id}:${requestedTopicId ?? ""}`;
  // Stable numbering must not change when a reply changes activity order.
  const orderedTopics = [...topics].sort((a, b) => (Date.parse(a.created_at) || 0) - (Date.parse(b.created_at) || 0));
  const topicId = !topicError ? topics.find((topic) => topic.conversation_id === conversation?.id && (requestedTopicId === null || topic.id === requestedTopicId))?.id ?? "" : "";
  const invalidTopic = !!conversation && topicsLoaded && requestedTopicId !== null && !topicId;
  // Pin the default topic in the URL so activity cannot silently retarget a draft.
  useEffect(() => {
    if (conversation && topicId && requestedTopicId === null) {
      replace(`${pathname}?${new URLSearchParams({ conversation: conversation.id, topic: topicId })}`);
    }
  }, [conversation, topicId, requestedTopicId, replace, pathname]);
  const messagesQuery = useQuery(topicMessagesOptions(wsId, conversation ? topicId : ""));
  const { data: messages = [], error: messageError } = messagesQuery;
  const { data: runs = [] } = useQuery(topicRunsOptions(wsId, conversation ? topicId : ""));
  const cancelRun = useCancelTopicRun(wsId);
  const { getActorName: getName } = useActorName();
  const send = useSendTopicMessage(wsId);
  const createTopic = useCreateTopic(wsId);
  const [error, setError] = useState<string | null>(null);
  const choose = (convId: string, topic?: string) => {
    const query = new URLSearchParams({ conversation: convId });
    if (topic) query.set("topic", topic);
    push(`${paths.chat()}?${query}`);
    setError(null);
  };
  const name = (conv: typeof conversations[number]) => conv.title || conv.participants.filter((p) => p.principal_id !== userId).map((p) => getName(p.principal_type, p.principal_id)).join(", ");
  return (
        <main className="@container flex min-h-0 min-w-0 flex-1 flex-col">
          {(error || listError || topicError || invalidTopic || messageError || (listQuery.isSuccess && !conversation)) && <div role="alert" className="flex items-center gap-2 px-4 py-2 text-body text-destructive">
            <span>{error ?? t(($) => $.groups.load_failed)}</span>
            <Button variant="outline" size="sm" onClick={() => { setError(null); void listQuery.refetch(); if (conversation) void topicsQuery.refetch(); if (topicId) void messagesQuery.refetch(); }}>{t(($) => $.groups.retry)}</Button>
          </div>}
          {conversation ? <>
            <div className="flex flex-wrap items-center gap-2 px-4 py-2">
              <h2 className="min-w-0 flex-1 truncate text-body font-semibold">{name(conversation)}</h2>
              <GroupInfoDialog key={conversation.id} conversation={conversation} onLeft={() => { if (selectedIdRef.current === conversation.id) replace(paths.chat()); }} />
              <Button variant="outline" disabled={createTopic.isPending} onClick={async () => {
                setError(null);
                const selection = selectionRef.current;
                try { const topic = await createTopic.mutateAsync({ id: conversation.id, title: "" }); if (selectedIdRef.current === conversation.id && selectionRef.current === selection) choose(conversation.id, topic.id); }
                catch { if (selectedIdRef.current === conversation.id && selectionRef.current === selection) setError(t(($) => $.groups.create_failed)); }
              }}>{t(($) => $.groups.new_topic)}</Button>
            </div>
            <p className="px-4 text-caption text-muted-foreground">{conversation.participants.map((p) => getName(p.principal_type, p.principal_id)).join(", ")}</p>
            {(topics.length > 1 || topics.some((topic) => topic.title?.trim())) && <nav aria-label={t(($) => $.groups.topics)} className="flex shrink-0 gap-1 overflow-x-auto px-4 py-2">
              {orderedTopics.map((topic, index) => <AppLink key={topic.id} href={`${paths.chat()}?conversation=${conversation.id}&topic=${topic.id}`} aria-current={topicId === topic.id ? "page" : undefined} className={buttonVariants({ variant: topicId === topic.id ? "secondary" : "ghost" })}>{topic.title?.trim() || t(($) => $.groups.numbered_topic, { number: index + 1 })}</AppLink>)}
            </nav>}
            <div className="min-h-0 flex-1 overflow-y-auto p-4" aria-live="polite">
              {(topicsQuery.isPending || (!!topicId && messagesQuery.isPending)) && <p role="status" className="text-body text-muted-foreground">{t(($) => $.groups.loading)}</p>}
              {runs.filter((run) => isTopicRunActive(run.status) || run.status === "failed" || run.status === "cancelled").map((run) => <div key={run.id} className="mb-2 flex flex-wrap items-center gap-2 text-caption">
                <span>{getName("agent", run.agent_id)} · {run.status}</span>
                {run.error && <span className="text-destructive">{run.error}</span>}
                {isTopicRunActive(run.status) && <Button size="xs" variant="outline" disabled={cancelRun.isPending} onClick={async () => { try { await cancelRun.mutateAsync(run.id); } catch { setError(t(($) => $.groups.change_failed)); } }}>{t(($) => $.groups.stop)}</Button>}
              </div>)}
              {!messageError && messages.map((message) => <article key={message.id} className="mb-5">
                <header className="mb-1 text-caption text-muted-foreground">{getName(message.sender_type, message.sender_id)}</header>
                <ReadonlyContent content={message.content} attachments={message.attachments} />
                {(message.attachments ?? []).filter((att) => !message.content.includes(`/attachments/${att.id}/download`) && !message.content.includes(att.url)).map((att) => <Attachment key={att.id} attachment={{ kind: "record", attachment: att }} />)}
              </article>)}
            </div>
            {topicId && !messageError && <>
              <ChatInput
                draftKeyOverride={`group:${topicId}`}
                editorKeyOverride="group-composer"
                uploadEnabled
                uploadTopicId={topicId}
                disabled={send.isPending}
                onSend={async (content, attachmentIds, commit) => {
                  setError(null);
                  if ((attachmentIds?.length ?? 0) > CHAT_ATTACHMENT_LIMITS.maxFiles) { setError(t(($) => $.groups.attachment_limit, { count: CHAT_ATTACHMENT_LIMITS.maxFiles, size: CHAT_ATTACHMENT_LIMITS.maxBytes / (1024 * 1024) })); return false; }
                  try { await send.mutateAsync({ id: topicId, content, attachmentIds }); commit(); return true; }
                  catch { setError(t(($) => $.groups.send_failed)); return false; }
                }}
              />
              <div className={`${CHAT_GUTTER} shrink-0 pb-3 text-caption text-muted-foreground`}>
                <div className={CHAT_COLUMN}>
                  {conversation.kind === "group" && <p>{t(($) => $.groups.mention_hint)}</p>}
                  <p>{t(($) => $.groups.attachment_limit, { count: CHAT_ATTACHMENT_LIMITS.maxFiles, size: CHAT_ATTACHMENT_LIMITS.maxBytes / (1024 * 1024) })}</p>
                </div>
              </div>
            </>}
          </> : <p role="status" className="m-auto p-4 text-body text-muted-foreground">{listQuery.isPending ? t(($) => $.groups.loading) : t(($) => $.groups.choose)}</p>}
        </main>
  );
}

export function CreateGroupDialog({ onCreated }: { onCreated: (id: string) => void }) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id);
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const create = useCreateConversation(wsId);
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [selected, setSelected] = useState<Principal[]>([]);
  const [error, setError] = useState(false);
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const toggle = (principal: Principal) => setSelected((old) => old.some((p) => p.type === principal.type && p.id === principal.id) ? old.filter((p) => p.type !== principal.type || p.id !== principal.id) : [...old, principal]);
  const checkbox = (principal: Principal, name: string) => <label key={principal.type + principal.id} className="flex items-center gap-2 py-1 text-body">
    <input type="checkbox" checked={selected.some((p) => p.type === principal.type && p.id === principal.id)} disabled={create.isPending} onChange={() => toggle(principal)} />{name}
  </label>;
  return <Dialog open={open} onOpenChange={(next) => { if (!create.isPending) setOpen(next); }}>
    <DialogTrigger render={<Button />}>{t(($) => $.groups.create)}</DialogTrigger>
    <DialogContent>
      <DialogHeader><DialogTitle>{t(($) => $.groups.create)}</DialogTitle></DialogHeader>
      <label className="text-body">{t(($) => $.groups.name)}<Input value={title} disabled={create.isPending} onChange={(e) => setTitle(e.target.value)} /></label>
      <div className="max-h-72 overflow-y-auto">
        <fieldset><legend className="text-caption text-muted-foreground">{t(($) => $.groups.people)}</legend>{members.filter((m) => m.user_id !== userId).map((m) => checkbox({ type: "member", id: m.user_id }, m.name))}</fieldset>
        <fieldset><legend className="text-caption text-muted-foreground">{t(($) => $.groups.agents)}</legend>{agents.filter((a) => !a.archived_at).map((a) => checkbox({ type: "agent", id: a.id }, a.name))}</fieldset>
      </div>
      {error && <p role="alert" className="text-body text-destructive">{t(($) => $.groups.create_failed)}</p>}
      <DialogFooter>
        <Button variant="outline" disabled={create.isPending} onClick={() => setOpen(false)}>{t(($) => $.groups.cancel)}</Button>
        <Button disabled={create.isPending || !title.trim() || selected.length === 0} aria-busy={create.isPending} onClick={async () => {
          setError(false);
          try { const conv = await create.mutateAsync({ kind: "group", title: title.trim(), participants: selected }); if (!mounted.current) return; setOpen(false); setTitle(""); setSelected([]); onCreated(conv.id); }
          catch { if (mounted.current) setError(true); }
        }}>{t(($) => $.groups.create)}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
