import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/http';
import { MockApi } from '../api/mock';
import type { Api } from '../api/types';
import { App } from './App';

beforeEach(() => {
  history.replaceState(null, '', '/');
  try {
    localStorage.clear();
  } catch {
    /* no storage */
  }
});

describe('App', () => {
  it('renders the overview from the API', async () => {
    render(<App api={new MockApi()} />);
    expect(await screen.findByText('Network activity')).toBeInTheDocument();
    expect(screen.getByText('Presence')).toBeInTheDocument();
  });

  it('asks for the URL with the token when the daemon answers 401', async () => {
    const api = new MockApi() as unknown as Api;
    const denied = () => Promise.reject(new ApiError(401, 'token required'));
    vi.spyOn(api, 'status').mockImplementation(denied);
    vi.spyOn(api, 'interfaces').mockImplementation(denied);
    render(<App api={api} />);
    expect(await screen.findByText('sudo lan-tracker ui url')).toBeInTheDocument();
  });

  it('navigates to a device and saves its description through the API', async () => {
    const api = new MockApi();
    const set = vi.spyOn(api, 'setDescription');
    const user = userEvent.setup();
    render(<App api={api} />);
    await user.click(await screen.findByRole('button', { name: /^Devices/ }));
    const table = await screen.findByRole('table');
    const first = within(table).getAllByRole('row')[1]!;
    await user.click(first);
    await user.click(await screen.findByRole('button', { name: /Edit|Describe this device/ }));
    const box = screen.getByLabelText('Description');
    await user.clear(box);
    await user.type(box, 'Pump 3');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(set).toHaveBeenCalledWith(expect.any(String), 'Pump 3');
  });
});
