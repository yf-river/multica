// @vitest-environment node
import { describe, expect, it } from 'vitest';
import {repositoryIdentity, repositoryProvider, repositoryStatus} from './repository-identity';
describe('repository metadata', () => {
 it('equates GitHub case and clone protocols while preserving other hosts path case', () => {
  expect(repositoryIdentity('git@GitHub.com:Acme/APP.git')).toBe(repositoryIdentity('https://github.com/acme/app'));
  expect(repositoryIdentity('https://git.code.tencent.com/Acme/APP.git')).not.toBe(repositoryIdentity('https://git.code.tencent.com/acme/app.git'));
  expect(repositoryIdentity('file:///tmp/app')).toBeNull();
 });
 it('recognizes old URL-only repositories and future provider values', () => {
  expect(repositoryProvider({url:'https://github.com/acme/app'})).toBe('github');
  expect(repositoryProvider({url:'https://git.code.tencent.com/acme/app'})).toBe('gongfeng');
  expect(repositoryProvider({url:'https://github.com/acme/app',provider:'future'})).toBe('unknown');
 });
 it('accepts server verification values and prioritizes failure over success', () => {
  expect(repositoryStatus({url:'x',test_status:'passed',connection_status:'credential_backed'})).toBe('ok');
  expect(repositoryStatus({url:'x',test_status:'passed',sync_status:'failed'})).toBe('error');
  expect(repositoryStatus({url:'x',test_status:'future'})).toBe('pending');
 });
});
