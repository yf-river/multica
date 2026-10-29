-- 固定测试样本；账户密码和运行时绑定由各环境在启动时设置。
INSERT INTO public."user" (id, name, account, email)
VALUES ('00000000-0000-4000-8000-000000000001', '测试用户', 'verification', 'verification@example.invalid');

INSERT INTO public.workspace (id, name, slug, issue_prefix, issue_counter)
VALUES ('00000000-0000-4000-8000-000000000002', '测试工作区', 'verification', 'TEST', 1);

INSERT INTO public.member (id, workspace_id, user_id, role)
VALUES ('00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000001', 'owner');

INSERT INTO public.project (id, workspace_id, title)
VALUES ('00000000-0000-4000-8000-000000000004', '00000000-0000-4000-8000-000000000002', '验收项目');

INSERT INTO public.agent (id, workspace_id, name, runtime_mode, owner_id, model)
VALUES ('00000000-0000-4000-8000-000000000005', '00000000-0000-4000-8000-000000000002', '验收 Agent', 'local', '00000000-0000-4000-8000-000000000001', 'deepseek-v4.1-flash');

INSERT INTO public.issue (id, workspace_id, title, status, creator_type, creator_id, project_id, number, assignee_type, assignee_id)
VALUES ('00000000-0000-4000-8000-000000000006', '00000000-0000-4000-8000-000000000002', '验收样本任务', 'todo', 'member', '00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000004', 1, 'agent', '00000000-0000-4000-8000-000000000005');

INSERT INTO public.comment (id, workspace_id, issue_id, author_type, author_id, content)
VALUES ('00000000-0000-4000-8000-000000000007', '00000000-0000-4000-8000-000000000002', '00000000-0000-4000-8000-000000000006', 'member', '00000000-0000-4000-8000-000000000001', '验收样本评论');
