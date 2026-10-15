// @vitest-environment jsdom
import { createElement, type ReactNode } from 'react';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { repositoryKeys, useRepositoryActions } from './repository-queries';
import { workspaceKeys } from './queries';
vi.mock('../api', () => ({api: {createRepository:vi.fn()}}));
describe('repository save cache', () => {
 it('adds through the registry and invalidates both views without replacing project bindings', async () => {
  const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});
  const original={id:'workspace',repos:[{url:'https://github.com/a/old'}]};
  const updated={...original,repos:[{url:'https://github.com/a/new',provider:'github',default_branch:'main'}]};
  const projects=[{id:'project',title:'Project',resources:[]}];
  client.setQueryData(workspaceKeys.list(),[original]);
  client.setQueryData(repositoryKeys.center('workspace'),{repos:original.repos,projects,locations:[]});
  vi.mocked(api.createRepository).mockResolvedValue(updated.repos[0] as Awaited<ReturnType<typeof api.createRepository>>);
  const wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children);
  const {result,unmount}=renderHook(()=>useRepositoryActions('workspace'),{wrapper});
  await act(async()=>{await result.current.save.mutateAsync(updated.repos)});
  expect(api.createRepository).toHaveBeenCalledWith("workspace",updated.repos[0]);
  expect(client.getQueryData(workspaceKeys.list())).toEqual([original]);
  expect(client.getQueryData(repositoryKeys.center('workspace'))).toEqual({repos:original.repos,projects,locations:[]});
  expect(client.getQueryState(repositoryKeys.center('workspace'))?.isInvalidated).toBe(true);
  unmount();client.clear();
 });
});
