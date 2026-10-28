import { useState } from 'react';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DestinationSummary, HeartbeatCheck, RouteSummary } from '../api/deliveryClient';
import { strings } from '../strings';
import { AddAppForm } from './AddAppForm';
import { AddDestinationForm } from './AddDestinationForm';
import { AppCombobox } from './AppCombobox';
import { HeartbeatForm } from './HeartbeatForm';
import { ProvisionForm } from './ProvisionForm';
import { RouteForm } from './RouteForm';

afterEach(() => {
  cleanup();
});

const DESTINATIONS = [
  {
    name: 'ops_discord',
    type: 'discord',
    enabled: true,
    disabledReason: null,
    config: {},
    secrets: {},
    ownerKind: 'operator',
    ownerAppId: null,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
  } as DestinationSummary,
];

/** Types `value` into a field while an IME composition is active (no compositionend). */
function typeMidComposition({ input, value }: { input: HTMLElement; value: string }) {
  fireEvent.compositionStart(input);
  fireEvent.change(input, { target: { value } });
  expect(input).toHaveValue(value);
}

function expectLowercaseAttributes(input: HTMLElement) {
  expect(input).toHaveAttribute('autocapitalize', 'none');
  expect(input).toHaveAttribute('autocorrect', 'off');
  expect(input).toHaveAttribute('spellcheck', 'false');
}

describe('HeartbeatForm add', () => {
  function renderAdd() {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <HeartbeatForm
        destinations={DESTINATIONS}
        destinationsReady
        onSubmit={onSubmit}
        onClose={vi.fn()}
      />,
    );
    return { onSubmit };
  }

  it('lowercases app id and check name as typed and sets the keyboard attributes', () => {
    renderAdd();
    const app = screen.getByLabelText(strings.heartbeatAppIdLabel);
    const name = screen.getByLabelText(strings.heartbeatNameLabel);
    fireEvent.change(app, { target: { value: 'MyApp' } });
    fireEvent.change(name, { target: { value: 'Site' } });
    expect(app).toHaveValue('myapp');
    expect(name).toHaveValue('site');
    expectLowercaseAttributes(app);
    expectLowercaseAttributes(name);
  });

  it('submits the lowercased value when Save is pressed mid-composition', async () => {
    const { onSubmit } = renderAdd();
    const app = screen.getByLabelText(strings.heartbeatAppIdLabel);
    const name = screen.getByLabelText(strings.heartbeatNameLabel);
    typeMidComposition({ input: app, value: 'MyApp' });
    typeMidComposition({ input: name, value: 'Site' });
    fireEvent.click(screen.getByRole('checkbox', { name: /^ops_discord/ }));
    fireEvent.submit(app.closest('form') as HTMLFormElement);
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0][0]).toMatchObject({ appId: 'myapp', name: 'site' });
    expect(app).toHaveValue('myapp');
    expect(name).toHaveValue('site');
  });
});

describe('HeartbeatForm edit', () => {
  it('renders no app id or name input to transform', () => {
    const check = {
      id: 7,
      appId: 'zqapp',
      name: 'site',
      periodSeconds: 3600,
      graceSeconds: 600,
      paused: false,
      state: 'up',
      lastPingAt: null,
      lastPingKind: null,
      downSince: null,
      deadlineAt: '2099-01-01T10:00:00Z',
      destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
      ownerKind: 'operator',
      ownerAppId: null,
      downReason: null,
      probe: null,
      createdAt: '2026-10-17T10:00:00Z',
      updatedAt: '2026-10-18T08:01:00Z',
    } as HeartbeatCheck;
    render(
      <HeartbeatForm
        check={check}
        destinations={DESTINATIONS}
        destinationsReady
        onSubmit={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.queryByLabelText(strings.heartbeatAppIdLabel)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(strings.heartbeatNameLabel)).not.toBeInTheDocument();
  });
});

describe('RouteForm', () => {
  function renderAdd() {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<RouteForm destinations={DESTINATIONS} onSubmit={onSubmit} onClose={vi.fn()} />);
    return { onSubmit };
  }

  it('add lowercases app and channel patterns and keeps wildcards', () => {
    renderAdd();
    const app = screen.getByLabelText(strings.routeAppLabel);
    const channel = screen.getByLabelText(strings.routeChannelLabel);
    fireEvent.change(app, { target: { value: 'MyApp' } });
    fireEvent.change(channel, { target: { value: 'Alerts*' } });
    expect(app).toHaveValue('myapp');
    expect(channel).toHaveValue('alerts*');
    fireEvent.change(app, { target: { value: '*' } });
    expect(app).toHaveValue('*');
    expectLowercaseAttributes(app);
    expectLowercaseAttributes(channel);
  });

  it('add submits the lowercased values when Save is pressed mid-composition', async () => {
    const { onSubmit } = renderAdd();
    const app = screen.getByLabelText(strings.routeAppLabel);
    const channel = screen.getByLabelText(strings.routeChannelLabel);
    typeMidComposition({ input: app, value: 'MyApp' });
    typeMidComposition({ input: channel, value: 'Alerts' });
    fireEvent.click(screen.getByRole('checkbox', { name: /^ops_discord/ }));
    fireEvent.submit(app.closest('form') as HTMLFormElement);
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0][0]).toMatchObject({
      appPattern: 'myapp',
      channelPattern: 'alerts',
    });
    expect(app).toHaveValue('myapp');
    expect(channel).toHaveValue('alerts');
  });

  it('edit keeps capitals in both pattern inputs', () => {
    const route = {
      id: 3,
      appPattern: 'myapp',
      channelPattern: 'alerts',
      destinations: ['ops_discord'],
      enabled: true,
    } as RouteSummary;
    render(
      <RouteForm route={route} destinations={DESTINATIONS} onSubmit={vi.fn()} onClose={vi.fn()} />,
    );
    const app = screen.getByLabelText(strings.routeAppLabel);
    const channel = screen.getByLabelText(strings.routeChannelLabel);
    fireEvent.change(app, { target: { value: 'MyApp' } });
    fireEvent.change(channel, { target: { value: 'Alerts' } });
    expect(app).toHaveValue('MyApp');
    expect(channel).toHaveValue('Alerts');
    expect(app).not.toHaveAttribute('autocapitalize');
  });
});

describe('AddAppForm', () => {
  it('lowercases the app id but keeps display name and description case', () => {
    render(<AddAppForm onAddApp={vi.fn()} />);
    const appId = screen.getByLabelText(strings.appIdLabel);
    const display = screen.getByLabelText(strings.appDisplayNameLabel);
    const description = screen.getByLabelText(strings.appDescriptionLabel);
    fireEvent.change(appId, { target: { value: 'TaskTracker' } });
    fireEvent.change(display, { target: { value: 'Task Tracker' } });
    fireEvent.change(description, { target: { value: 'Tracks Tasks' } });
    expect(appId).toHaveValue('tasktracker');
    expect(display).toHaveValue('Task Tracker');
    expect(description).toHaveValue('Tracks Tasks');
    expectLowercaseAttributes(appId);
  });

  it('submits the lowercased id when Save is pressed mid-composition', async () => {
    const onAddApp = vi.fn().mockResolvedValue({
      appId: 'tasktracker',
      publisherUserId: 'p',
      topicPattern: 't',
      token: 'tk',
    });
    render(<AddAppForm onAddApp={onAddApp} />);
    const appId = screen.getByLabelText(strings.appIdLabel);
    typeMidComposition({ input: appId, value: 'TaskTracker' });
    fireEvent.change(screen.getByLabelText(strings.appDisplayNameLabel), {
      target: { value: 'Task Tracker' },
    });
    fireEvent.click(screen.getByRole('button', { name: strings.addAppAction }));
    await waitFor(() => expect(onAddApp).toHaveBeenCalledTimes(1));
    expect(onAddApp.mock.calls[0][0]).toMatchObject({ appId: 'tasktracker' });
    expect(appId).toHaveValue('tasktracker');
  });
});

describe('AddDestinationForm', () => {
  it('lowercases the name as typed', () => {
    render(<AddDestinationForm onAdd={vi.fn()} onCancel={vi.fn()} />);
    const name = screen.getByLabelText(strings.destNameLabel);
    fireEvent.change(name, { target: { value: 'Ops_Discord' } });
    expect(name).toHaveValue('ops_discord');
    expectLowercaseAttributes(name);
  });

  it('submits the lowercased name when Add is pressed mid-composition', async () => {
    const onAdd = vi.fn().mockResolvedValue(undefined);
    render(<AddDestinationForm onAdd={onAdd} onCancel={vi.fn()} />);
    const name = screen.getByLabelText(strings.destNameLabel);
    typeMidComposition({ input: name, value: 'OpsNtfy' });
    fireEvent.change(screen.getByLabelText(strings.destTopicLabel), {
      target: { value: 'Topic_One' },
    });
    fireEvent.click(screen.getByRole('button', { name: strings.addDestinationAction }));
    await waitFor(() => expect(onAdd).toHaveBeenCalledTimes(1));
    expect(onAdd.mock.calls[0][0].params.name).toBe('opsntfy');
    expect(name).toHaveValue('opsntfy');
    // Config values such as the topic keep their case.
    expect(onAdd.mock.calls[0][0].params.config.topic).toBe('Topic_One');
  });
});

describe('ProvisionForm', () => {
  it('lowercases the app id but keeps the email case', () => {
    render(<ProvisionForm onProvision={vi.fn()} />);
    const appId = screen.getByLabelText(strings.appIdLabel);
    const email = screen.getByLabelText(strings.emailLabel);
    fireEvent.change(appId, { target: { value: 'MyApp' } });
    fireEvent.change(email, { target: { value: 'Alice@Example.com' } });
    expect(appId).toHaveValue('myapp');
    expect(email).toHaveValue('Alice@Example.com');
    expectLowercaseAttributes(appId);
  });

  it('provisions the lowercased app id when Provision is pressed mid-composition', async () => {
    const onProvision = vi.fn().mockResolvedValue({
      userId: 'u',
      appId: 'myapp',
      token: 'tk',
    });
    render(<ProvisionForm onProvision={onProvision} />);
    const appId = screen.getByLabelText(strings.appIdLabel);
    typeMidComposition({ input: appId, value: 'MyApp' });
    fireEvent.change(screen.getByLabelText(strings.emailLabel), {
      target: { value: 'alice@example.com' },
    });
    fireEvent.click(screen.getByRole('button', { name: strings.provisionAction }));
    await waitFor(() => expect(onProvision).toHaveBeenCalledTimes(1));
    expect(onProvision.mock.calls[0][0]).toMatchObject({ appId: 'myapp' });
    expect(appId).toHaveValue('myapp');
  });
});

describe('AppCombobox lowercase prop', () => {
  function Harness({ lowercase }: { lowercase?: boolean }) {
    const [value, setValue] = useState('');
    return (
      <AppCombobox id="app-id" value={value} onChange={setValue} apps={[]} lowercase={lowercase} />
    );
  }

  it('keeps capitals without the prop', () => {
    render(<Harness />);
    const input = within(document.body).getByRole('combobox');
    fireEvent.change(input, { target: { value: 'MyApp' } });
    expect(input).toHaveValue('MyApp');
    expect(input).not.toHaveAttribute('autocapitalize');
  });

  it('lowercases with the prop', () => {
    render(<Harness lowercase />);
    const input = within(document.body).getByRole('combobox');
    fireEvent.change(input, { target: { value: 'MyApp' } });
    expect(input).toHaveValue('myapp');
  });
});
