"use client";

import { useEffect, useState, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspacePaths } from "@multica/core/paths";
import { conversationsOptions, topicsOptions, topicMessagesOptions, topicRunsOptions, isTopicRunActive, useCancelTopicRun, useCreateConversation, useCreateTopic, useSendTopicMessage } from "@multica/core/chat-v2";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import type { Principal } from "@multica/core/chat-v2";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@multica/ui/components/ui/dialog";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../i18n";
import { AppLink, useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { ReadonlyContent } from "../editor";
import { Attachment } from "../editor/attachment";
import { ChatInput } from "./components/chat-input";
import { hrBatchReviewForAgent } from "./hr-batch-review";
import { GroupInfoDialog } from "./group-info-dialog";

export function ChatGroupsPage() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { searchParams, replace } = useNavigation();
  const userId = useAuthStore((s) => s.user?.id);
  const { data: conversations = [], error: listError } = useQuery(conversationsOptions(wsId));
  const id = searchParams.get("conversation") ?? "";
  const selectedIdRef = useRef(id);
  selectedIdRef.current = id;
  const conversation = conversations.find((item) => item.id === id && item.kind === "group");
  const { data: topics = [], error: topicError, isSuccess: topicsLoaded } = useQuery(topicsOptions(wsId, conversation?.id ?? ""));
  const requestedTopicId = searchParams.get("topic");
  const topicId = topics.find((topic) => topic.conversation_id === conversation?.id && (requestedTopicId === null || topic.id === requestedTopicId))?.id ?? "";
  const invalidTopic = !!conversation && topicsLoaded && requestedTopicId !== null && !topicId;
  // Pin the default topic in the URL so activity cannot silently retarget a draft.
  useEffect(() => {
    if (conversation && topicId && requestedTopicId === null) {
      replace(`${paths.chatGroups()}?${new URLSearchParams({ conversation: conversation.id, topic: topicId })}`);
    }
  }, [conversation, topicId, requestedTopicId, replace, paths]);
  const { data: messages = [], error: messageError } = useQuery(topicMessagesOptions(wsId, conversation ? topicId : ""));
  const { data: runs = [] } = useQuery(topicRunsOptions(wsId, conversation ? topicId : ""));
  const cancelRun = useCancelTopicRun(wsId);
  const { getActorName: getName } = useActorName();
  const send = useSendTopicMessage(wsId);
  const createTopic = useCreateTopic(wsId);
  const [error, setError] = useState<string | null>(null);
  const [reviewRequest, setReviewRequest] = useState<{ id: number; content: string; mode: "append"; topicId: string } | null>(null);
  const choose = (convId: string, topic?: string) => {
    const query = new URLSearchParams({ conversation: convId });
    if (topic) query.set("topic", topic);
    replace(`${paths.chatGroups()}?${query}`);
    setError(null);
  };
  const name = (conv: typeof conversations[number]) => conv.title || conv.participants.filter((p) => p.principal_id !== userId).map((p) => getName(p.principal_type, p.principal_id)).join(", ");
  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <h1 className="flex-1 text-body font-semibold">{t(($) => $.groups.title)}</h1>
        <AppLink href={paths.chat()}>{t(($) => $.groups.direct_chats)}</AppLink>
        <CreateGroupDialog onCreated={choose} />
      </PageHeader>
      {(error || listError || topicError || invalidTopic || messageError) && <p role="alert" className="px-4 py-2 text-body text-destructive">{error ?? t(($) => $.groups.load_failed)}</p>}
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav aria-label={t(($) => $.groups.title)} className="max-h-48 w-full shrink-0 overflow-y-auto p-2 md:max-h-none md:w-64">
          {conversations.filter((conv) => conv.kind === "group").map((conv) => <AppLink key={conv.id} href={`${paths.chatGroups()}?conversation=${conv.id}`} aria-current={id === conv.id ? "page" : undefined} className={buttonVariants({ variant: id === conv.id ? "secondary" : "ghost", className: "mb-1 w-full justify-start truncate" })}>{name(conv) || t(($) => $.groups.untitled)}</AppLink>)}
        </nav>
        <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {conversation ? <>
            <div className="flex flex-wrap items-center gap-2 px-4 py-2">
              <h2 className="min-w-0 flex-1 truncate text-body font-semibold">{name(conversation)}</h2>
              <GroupInfoDialog key={conversation.id} conversation={conversation} onLeft={() => { if (selectedIdRef.current === conversation.id) replace(paths.chatGroups()); }} />
              <Button variant="outline" disabled={createTopic.isPending} onClick={async () => {
                setError(null);
                try { const topic = await createTopic.mutateAsync({ id: conversation.id, title: "" }); choose(conversation.id, topic.id); }
                catch { setError(t(($) => $.groups.create_failed)); }
              }}>{t(($) => $.groups.new_topic)}</Button>
            </div>
            <p className="px-4 text-caption text-muted-foreground">{conversation.participants.map((p) => getName(p.principal_type, p.principal_id)).join(", ")}</p>
            <nav aria-label={t(($) => $.groups.topics)} className="flex shrink-0 gap-1 overflow-x-auto px-4 py-2">
              {topics.map((topic) => <AppLink key={topic.id} href={`${paths.chatGroups()}?conversation=${conversation.id}&topic=${topic.id}`} aria-current={topicId === topic.id ? "page" : undefined} className={buttonVariants({ variant: topicId === topic.id ? "secondary" : "ghost" })}>{topic.title || t(($) => $.groups.untitled)}</AppLink>)}
            </nav>
            <div className="min-h-0 flex-1 overflow-y-auto p-4" aria-live="polite">
              {runs.filter((run) => isTopicRunActive(run.status) || run.status === "failed" || run.status === "cancelled").map((run) => <div key={run.id} className="mb-2 flex flex-wrap items-center gap-2 text-caption">
                <span>{getName("agent", run.agent_id)} · {run.status}</span>
                {run.error && <span className="text-destructive">{run.error}</span>}
                {isTopicRunActive(run.status) && <Button size="xs" variant="outline" disabled={cancelRun.isPending} onClick={async () => { try { await cancelRun.mutateAsync(run.id); } catch { setError(t(($) => $.groups.change_failed)); } }}>{t(($) => $.groups.stop)}</Button>}
              </div>)}
              {messages.map((message) => <article key={message.id} className="mb-5">
                <header className="mb-1 text-caption text-muted-foreground">{getName(message.sender_type, message.sender_id)}</header>
                <ReadonlyContent content={message.content} attachments={message.attachments} />
                {(message.attachments ?? []).filter((att) => !message.content.includes(`/attachments/${att.id}/download`) && !message.content.includes(att.url)).map((att) => <Attachment key={att.id} attachment={{ kind: "record", attachment: att }} />)}
              </article>)}
            </div>
            {topicId && <div className="shrink-0 p-3">
              <ChatInput
                draftKeyOverride={`group:${topicId}`}
                editorKeyOverride="group-composer"
                uploadEnabled
                uploadTopicId={topicId}
                disabled={send.isPending}
                leftAdornment={<DropdownMenu>
                  <DropdownMenuTrigger render={<Button variant="ghost" disabled={!conversation.participants.some((p) => p.principal_type === "agent")} />}>{t(($) => $.groups.review_batch)}</DropdownMenuTrigger>
                  <DropdownMenuContent>
                    <p className="max-w-64 px-2 py-1 text-caption text-muted-foreground">{t(($) => $.groups.review_hint)}</p>
                    {conversation.participants.filter((p) => p.principal_type === "agent").map((p) => <DropdownMenuItem key={p.principal_id} onClick={() => setReviewRequest({ id: Date.now(), topicId, mode: "append", content: hrBatchReviewForAgent(p.principal_id, getName("agent", p.principal_id)) })}>{getName("agent", p.principal_id)}</DropdownMenuItem>)}
                  </DropdownMenuContent>
                </DropdownMenu>}
                conversationStarterRequest={reviewRequest?.topicId === topicId ? reviewRequest : null}
                onConversationStarterApplied={() => setReviewRequest(null)}
                onSend={async (content, attachmentIds, commit) => {
                  setError(null);
                  if ((attachmentIds?.length ?? 0) > 20) { setError(t(($) => $.groups.attachment_limit)); return false; }
                  try { await send.mutateAsync({ id: topicId, content, attachmentIds }); commit(); return true; }
                  catch { setError(t(($) => $.groups.send_failed)); return false; }
                }}
              />
              {conversation.kind === "group" && <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.groups.mention_hint)}</p>}
              <p className="text-caption text-muted-foreground">{t(($) => $.groups.attachment_limit)}</p>
            </div>}
          </> : <p className="m-auto p-4 text-body text-muted-foreground">{t(($) => $.groups.choose)}</p>}
        </main>
      </div>
    </div>
  );
}

function CreateGroupDialog({ onCreated }: { onCreated: (id: string) => void }) {
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
  const toggle = (principal: Principal) => setSelected((old) => old.some((p) => p.type === principal.type && p.id === principal.id) ? old.filter((p) => p.type !== principal.type || p.id !== principal.id) : [...old, principal]);
  const checkbox = (principal: Principal, name: string) => <label key={principal.type + principal.id} className="flex items-center gap-2 py-1 text-body">
    <input type="checkbox" checked={selected.some((p) => p.type === principal.type && p.id === principal.id)} disabled={create.isPending} onChange={() => toggle(principal)} />{name}
  </label>;
  return <Dialog open={open} onOpenChange={setOpen}>
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
          try { const conv = await create.mutateAsync({ kind: "group", title: title.trim(), participants: selected }); setOpen(false); setTitle(""); setSelected([]); onCreated(conv.id); }
          catch { setError(true); }
        }}>{t(($) => $.groups.create)}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
