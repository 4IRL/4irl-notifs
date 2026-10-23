import { strings } from '../strings';

/**
 * The two names of a row action. `visible` is what the button shows: the bare verb, because
 * the row it sits in already says which destination or route it acts on. `accessible` is its
 * `aria-label`: the verb plus the object, so a screen-reader user hears (and a voice-control
 * user can say) which row it belongs to. The accessible name always contains the visible
 * text (WCAG 2.5.3 Label in Name).
 */
export interface ActionLabel {
  visible: string;
  accessible: string;
}

/** The labels of a destination row's actions, inline buttons and menu items alike. */
export interface DestinationActionLabels {
  test: ActionLabel;
  /** "Edit", or "Close" while this row's editor is open. */
  edit: ActionLabel;
  disable: ActionLabel;
  confirmDisable: ActionLabel;
  cancelDisable: ActionLabel;
  enable: ActionLabel;
  delete: ActionLabel;
  confirmDelete: ActionLabel;
  cancelDelete: ActionLabel;
}

/** The labels of a route row's actions. */
export interface RouteActionLabels {
  /** "Edit", or "Close" while this row's editor is open. */
  edit: ActionLabel;
  delete: ActionLabel;
  confirmDelete: ActionLabel;
  cancelDelete: ActionLabel;
}

export function destinationActionLabels({
  name,
  editing,
}: {
  name: string;
  editing: boolean;
}): DestinationActionLabels {
  const disable = strings.disableDestination({ name });
  const deleteName = strings.deleteDestination({ name });
  return {
    test: { visible: strings.rowActionTest, accessible: strings.sendTestTo({ name }) },
    edit: editing
      ? { visible: strings.rowActionClose, accessible: strings.closeDestination({ name }) }
      : { visible: strings.rowActionEdit, accessible: strings.editDestination({ name }) },
    disable: { visible: strings.rowActionDisable, accessible: disable },
    confirmDisable: {
      visible: strings.rowActionConfirmDisable,
      accessible: strings.confirmDisableDestination({ name }),
    },
    cancelDisable: {
      visible: strings.cancelAction,
      accessible: strings.cancelActionNamed({ subject: disable }),
    },
    enable: { visible: strings.rowActionEnable, accessible: strings.enableDestination({ name }) },
    delete: { visible: strings.rowActionDelete, accessible: deleteName },
    confirmDelete: {
      visible: strings.rowActionConfirmDelete,
      accessible: strings.confirmDeleteDestination({ name }),
    },
    cancelDelete: {
      visible: strings.cancelAction,
      accessible: strings.cancelActionNamed({ subject: deleteName }),
    },
  };
}

export function routeActionLabels({
  id,
  editing,
}: {
  id: number;
  editing: boolean;
}): RouteActionLabels {
  const deleteName = strings.deleteRoute({ id });
  return {
    edit: editing
      ? { visible: strings.rowActionClose, accessible: strings.closeRoute({ id }) }
      : { visible: strings.rowActionEdit, accessible: strings.editRoute({ id }) },
    delete: { visible: strings.rowActionDelete, accessible: deleteName },
    confirmDelete: {
      visible: strings.rowActionConfirmDelete,
      accessible: strings.confirmDeleteRoute({ id }),
    },
    cancelDelete: {
      visible: strings.cancelAction,
      accessible: strings.cancelActionNamed({ subject: deleteName }),
    },
  };
}
